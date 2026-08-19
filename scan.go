package main

import (
	"encoding/binary"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/goburrow/modbus"
)

// ScanConfig controls how the GX device's Modbus-TCP server is probed.
type ScanConfig struct {
	Host      string
	Port      int
	Timeout   time.Duration
	Workers   int
	UnitIDMin int
	UnitIDMax int
}

// DeviceResult is everything read back from one connected Victron device
// (one D-Bus service instance, addressed by one Modbus unit ID).
type DeviceResult struct {
	UnitID         int          `json:"unitId"`
	DeviceInstance int          `json:"deviceInstance"`
	ServiceType    string       `json:"serviceType"`
	Registers      []FieldValue `json:"registers"`
}

// ScanResult is the top-level JSON document produced by a full scan.
type ScanResult struct {
	Host        string         `json:"host"`
	Port        int            `json:"port"`
	ScannedAt   string         `json:"scannedAt"`
	UnitIDRange [2]int         `json:"unitIdRange"`
	Devices     []DeviceResult `json:"devices"`
	Warnings    []string       `json:"warnings,omitempty"`
}

type serviceInfo struct {
	name string
	runs []RegisterRun
}

// Scan connects to the Victron GX device's Modbus-TCP server and, for every
// unit ID in [cfg.UnitIDMin, cfg.UnitIDMax], determines which (if any) of
// the known Victron D-Bus service classes responds there, then reads and
// decodes every documented register for that class.
//
// Discovery relies on Victron's own convention (see the GX Modbus-TCP
// manual): a Modbus unit ID addresses one D-Bus device instance directly
// (unit ID == device instance, with unit 100 aliasing device instance 0,
// the system service). Since each service class occupies its own
// non-overlapping block of register addresses in the published map, a
// service class is confirmed present at a unit ID only if that unit ID
// answers a read for that class's first register without a Modbus
// exception.
func Scan(cfg ScanConfig) (*ScanResult, error) {
	byService, err := LoadRegisterMap()
	if err != nil {
		return nil, err
	}
	aliases, err := LoadUnitIDAliases()
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(byService))
	for name := range byService {
		names = append(names, name)
	}
	sort.Strings(names)

	services := make([]serviceInfo, 0, len(names))
	for _, name := range names {
		runs := BuildRuns(byService[name])
		if len(runs) == 0 {
			continue
		}
		services = append(services, serviceInfo{name: name, runs: runs})
	}

	if err := preflight(cfg); err != nil {
		return nil, err
	}

	unitIDs := make([]int, 0, cfg.UnitIDMax-cfg.UnitIDMin+1)
	for id := cfg.UnitIDMin; id <= cfg.UnitIDMax; id++ {
		unitIDs = append(unitIDs, id)
	}

	jobs := make(chan int, len(unitIDs))
	for _, id := range unitIDs {
		jobs <- id
	}
	close(jobs)

	results := make(chan DeviceResult)
	warnings := make(chan string)
	var wg sync.WaitGroup

	workers := cfg.Workers
	if workers < 1 {
		workers = 1
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go scanWorker(cfg, services, jobs, results, warnings, &wg)
	}
	go func() {
		wg.Wait()
		close(results)
		close(warnings)
	}()

	res := &ScanResult{Host: cfg.Host, Port: cfg.Port, UnitIDRange: [2]int{cfg.UnitIDMin, cfg.UnitIDMax}}
	resultsOpen, warningsOpen := true, true
	for resultsOpen || warningsOpen {
		select {
		case d, ok := <-results:
			if !ok {
				resultsOpen = false
				results = nil
				continue
			}
			res.Devices = append(res.Devices, d)
		case w, ok := <-warnings:
			if !ok {
				warningsOpen = false
				warnings = nil
				continue
			}
			res.Warnings = append(res.Warnings, w)
		}
	}

	sort.Slice(res.Devices, func(i, j int) bool { return res.Devices[i].UnitID < res.Devices[j].UnitID })
	for i := range res.Devices {
		dev := &res.Devices[i]
		dev.DeviceInstance = deviceInstanceForUnitID(dev.UnitID, aliases)
	}
	return res, nil
}

// preflight makes sure the gateway is reachable at all before spinning up
// the worker pool, so a wrong host/port fails fast with a clear message
// instead of every worker silently failing to connect.
func preflight(cfg ScanConfig) error {
	handler := modbus.NewTCPClientHandler(fmt.Sprintf("%s:%d", cfg.Host, cfg.Port))
	handler.Timeout = cfg.Timeout
	handler.IdleTimeout = 0
	if err := handler.Connect(); err != nil {
		return fmt.Errorf("could not reach Modbus-TCP server at %s:%d: %w", cfg.Host, cfg.Port, err)
	}
	handler.Close()
	return nil
}

func scanWorker(cfg ScanConfig, services []serviceInfo, jobs <-chan int, results chan<- DeviceResult, warnings chan<- string, wg *sync.WaitGroup) {
	defer wg.Done()

	handler := modbus.NewTCPClientHandler(fmt.Sprintf("%s:%d", cfg.Host, cfg.Port))
	handler.Timeout = cfg.Timeout
	handler.IdleTimeout = 0
	if err := handler.Connect(); err != nil {
		warnings <- fmt.Sprintf("worker could not connect: %v", err)
		return
	}
	defer handler.Close()
	client := modbus.NewClient(handler)

	reconnect := func() {
		handler.Close()
		handler.Connect()
	}

	for unitID := range jobs {
		handler.SlaveId = byte(unitID)

		for _, svc := range services {
			probe := svc.runs[0]
			probeLen := probe.Length
			if probeLen > maxChunk {
				probeLen = maxChunk
			}
			if _, err := client.ReadHoldingRegisters(probe.Start, uint16(probeLen)); err != nil {
				if !isModbusException(err) {
					reconnect()
				}
				continue
			}

			fields, warns := readAllRuns(client, svc.runs)
			for _, w := range warns {
				warnings <- fmt.Sprintf("unit %d (%s): %s", unitID, svc.name, w)
			}
			results <- DeviceResult{UnitID: unitID, ServiceType: svc.name, Registers: fields}
			// Deliberately no break: most device instances host exactly one
			// service class, but the singleton services (system, settings,
			// platform, ...) all default to device instance 0 and so can
			// legitimately all answer at the same unit ID (0 or 100).
		}
	}
}

// readAllRuns reads every RegisterRun of a service, chunked to maxChunk
// registers per Modbus request, and decodes each field within it. A read
// failure only affects the fields whose registers fall inside that failed
// chunk (reported as unavailable with a note) - it does not discard fields
// already read successfully from other chunks or other runs.
func readAllRuns(client modbus.Client, runs []RegisterRun) ([]FieldValue, []string) {
	var out []FieldValue
	var warnings []string
	for _, run := range runs {
		words := make([]uint16, run.Length)
		failed := make([]bool, run.Length)
		offset := 0
		for offset < run.Length {
			chunkLen := run.Length - offset
			if chunkLen > maxChunk {
				chunkLen = maxChunk
			}
			addr := run.Start + uint16(offset)
			raw, err := client.ReadHoldingRegisters(addr, uint16(chunkLen))
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("read registers %d-%d: %v", addr, int(addr)+chunkLen-1, err))
				for i := offset; i < offset+chunkLen; i++ {
					failed[i] = true
				}
			} else {
				for i := 0; i < chunkLen; i++ {
					words[offset+i] = binary.BigEndian.Uint16(raw[i*2 : i*2+2])
				}
			}
			offset += chunkLen
		}
		for _, def := range run.Fields {
			start := int(def.Register) - int(run.Start)
			end := start + def.Length
			readable := true
			for i := start; i < end; i++ {
				if failed[i] {
					readable = false
					break
				}
			}
			if !readable {
				out = append(out, FieldValue{
					Path:     def.Path,
					Register: def.Register,
					DataType: dataTypeLabel(def),
					Access:   def.Access,
					Unit:     def.Unit,
					Note:     "register read failed (Modbus exception or timeout)",
				})
				continue
			}
			out = append(out, decodeField(def, words[start:end]))
		}
	}
	return out, warnings
}

// isModbusException reports whether err is a Modbus protocol-level
// exception response (e.g. "illegal data address" for a register this
// unit ID's device simply doesn't have) as opposed to a transport failure
// (timeout, reset connection) that warrants reconnecting.
func isModbusException(err error) bool {
	_, ok := err.(*modbus.ModbusError)
	return ok
}
