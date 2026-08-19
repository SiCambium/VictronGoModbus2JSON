// Command victron-modbus-json connects to a Victron Energy GX device (Cerbo
// GX, Venus GX, CCGX, ...) over Modbus-TCP, discovers which D-Bus services
// (battery monitors, solar chargers, VE.Bus inverter/chargers, etc.) are
// connected to it, and dumps every documented register for each one as JSON.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"
)

func main() {
	host := flag.String("host", "", "IP address or hostname of the Victron GX device (required)")
	port := flag.Int("port", 502, "Modbus-TCP port")
	timeout := flag.Duration("timeout", 800*time.Millisecond, "per-request timeout")
	workers := flag.Int("workers", 4, "number of concurrent Modbus-TCP connections used while scanning")
	unitMin := flag.Int("unit-min", 0, "lowest unit ID to scan")
	unitMax := flag.Int("unit-max", 247, "highest unit ID to scan (247 is the Modbus-TCP maximum Victron uses)")
	out := flag.String("out", "", "write JSON to this file instead of stdout")
	pretty := flag.Bool("pretty", true, "pretty-print the JSON output")
	flag.Parse()

	if *host == "" {
		fmt.Fprintln(os.Stderr, "error: -host is required, e.g. -host 172.22.25.100")
		flag.Usage()
		os.Exit(2)
	}

	cfg := ScanConfig{
		Host:      *host,
		Port:      *port,
		Timeout:   *timeout,
		Workers:   *workers,
		UnitIDMin: *unitMin,
		UnitIDMax: *unitMax,
	}

	fmt.Fprintf(os.Stderr, "Scanning %s:%d (unit IDs %d-%d, %d worker(s))...\n", cfg.Host, cfg.Port, cfg.UnitIDMin, cfg.UnitIDMax, cfg.Workers)

	result, err := Scan(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	result.ScannedAt = time.Now().Format(time.RFC3339)

	fmt.Fprintf(os.Stderr, "Found %d device(s).\n", len(result.Devices))
	for _, d := range result.Devices {
		fmt.Fprintf(os.Stderr, "  unit %-3d  %-32s  %d register(s)\n", d.UnitID, d.ServiceType, len(d.Registers))
	}
	for _, w := range result.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}

	var b []byte
	if *pretty {
		b, err = json.MarshalIndent(result, "", "  ")
	} else {
		b, err = json.Marshal(result)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error encoding JSON: %v\n", err)
		os.Exit(1)
	}
	b = append(b, '\n')

	if *out == "" {
		os.Stdout.Write(b)
		return
	}
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "error writing %s: %v\n", *out, err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "Wrote %s\n", *out)
}
