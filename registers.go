package main

import (
	_ "embed"
	"encoding/csv"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// The register map is Victron Energy's own published mapping of Modbus-TCP
// holding registers to D-Bus paths, one row per (service type, register).
// Source: https://github.com/victronenergy/dbus_modbustcp (attributes.csv, unitid2di.csv)
//
//go:embed data/attributes.csv
var attributesCSV []byte

//go:embed data/unitid2di.csv
var unitMapCSVData []byte

// RegisterDef describes a single Modbus holding register belonging to one
// Victron D-Bus service class (e.g. com.victronenergy.battery).
type RegisterDef struct {
	Service  string
	Path     string
	Register uint16
	Kind     string // uint16, int16, uint32, int32, string, reserved, internal
	Length   int    // number of consecutive 16-bit holding registers occupied
	Scale    float64
	Access   string
	Unit     string
	Enum     map[int]string
}

var enumTokenRe = regexp.MustCompile(`^-?\d+=`)
var dataTypeRe = regexp.MustCompile(`^([A-Za-z]+[0-9]*)(?:\[(\d+)\])?$`)

// parseMeaning splits the CSV "unit/meaning" column into either a plain
// physical unit string, or a set of enum value->label mappings. Victron's
// sheet uses the same column for both, distinguished only by content shape.
func parseMeaning(raw string) (unit string, enum map[int]string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	tokens := strings.Split(raw, ";")
	candidate := make(map[int]string, len(tokens))
	for _, tok := range tokens {
		tok = strings.TrimSpace(tok)
		if !enumTokenRe.MatchString(tok) {
			return raw, nil // not an enum shape -> treat whole thing as a unit string
		}
		parts := strings.SplitN(tok, "=", 2)
		n, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil {
			return raw, nil
		}
		candidate[n] = strings.TrimSpace(parts[1])
	}
	return "", candidate
}

// LoadRegisterMap parses the embedded attributes.csv into per-service,
// register-address-sorted lists of RegisterDef, skipping the RESERVED
// filler rows (they exist only to document unused address space).
func LoadRegisterMap() (map[string][]RegisterDef, error) {
	r := csv.NewReader(strings.NewReader(string(attributesCSV)))
	r.FieldsPerRecord = 8
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parsing attributes.csv: %w", err)
	}

	byService := make(map[string][]RegisterDef)
	for _, rec := range records {
		service, path, _typeIndicator, meaning, regStr, dataType, scaleStr, access := rec[0], rec[1], rec[2], rec[3], rec[4], rec[5], rec[6], rec[7]
		_ = _typeIndicator

		if path == "RESERVED" {
			continue
		}

		regNum, err := strconv.Atoi(strings.TrimSpace(regStr))
		if err != nil {
			return nil, fmt.Errorf("bad register %q for %s%s: %w", regStr, service, path, err)
		}

		m := dataTypeRe.FindStringSubmatch(strings.TrimSpace(dataType))
		if m == nil {
			return nil, fmt.Errorf("unrecognized data type %q for %s%s", dataType, service, path)
		}
		kind := strings.ToLower(m[1])
		length := 1
		if m[2] != "" {
			length, err = strconv.Atoi(m[2])
			if err != nil {
				return nil, fmt.Errorf("bad length in data type %q: %w", dataType, err)
			}
		} else if kind == "uint32" || kind == "int32" {
			length = 2
		}
		if kind == "internal" {
			kind = "internal"
		}

		scale := 1.0
		if s := strings.TrimSpace(scaleStr); s != "" {
			scale, err = strconv.ParseFloat(s, 64)
			if err != nil {
				return nil, fmt.Errorf("bad scale %q for %s%s: %w", scaleStr, service, path, err)
			}
		}

		unit, enum := parseMeaning(meaning)

		def := RegisterDef{
			Service:  service,
			Path:     path,
			Register: uint16(regNum),
			Kind:     kind,
			Length:   length,
			Scale:    scale,
			Access:   strings.TrimSpace(access),
			Unit:     unit,
			Enum:     enum,
		}
		byService[service] = append(byService[service], def)
	}

	for svc := range byService {
		defs := byService[svc]
		sort.Slice(defs, func(i, j int) bool { return defs[i].Register < defs[j].Register })
		byService[svc] = defs
	}
	return byService, nil
}

// UnitIDAlias documents Victron's legacy fixed-serial-port unit-ID table,
// used on older CCGX/Venus GX hardware where the underlying D-Bus device
// instance exceeds 247 (the maximum value a Modbus unit ID can hold) and so
// cannot be addressed directly as unitID == deviceInstance.
type UnitIDAlias struct {
	UnitID         int
	DeviceInstance int
	Remark         string
}

func LoadUnitIDAliases() ([]UnitIDAlias, error) {
	r := csv.NewReader(strings.NewReader(string(unitMapCSVData)))
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parsing unitid2di.csv: %w", err)
	}
	var out []UnitIDAlias
	for i, rec := range records {
		if i == 0 {
			continue // header
		}
		if len(rec) < 2 {
			continue
		}
		unitID, err := strconv.Atoi(strings.TrimSpace(rec[0]))
		if err != nil {
			continue
		}
		di, err := strconv.Atoi(strings.TrimSpace(rec[1]))
		if err != nil {
			continue
		}
		remark := ""
		if len(rec) >= 3 {
			remark = strings.TrimSpace(rec[2])
		}
		out = append(out, UnitIDAlias{UnitID: unitID, DeviceInstance: di, Remark: remark})
	}
	return out, nil
}

// deviceInstanceForUnitID returns the best-known D-Bus device instance for a
// given unit ID: the documented general rule is unitID == deviceInstance for
// instances 0-247 (with unit 100 reserved as an alias for instance 0, i.e.
// the system service, for clients that refuse to use unit ID 0). The
// unitid2di table overrides this only for legacy fixed hardware ports.
func deviceInstanceForUnitID(unitID int, aliases []UnitIDAlias) int {
	if unitID == 100 {
		return 0
	}
	for _, a := range aliases {
		if a.UnitID == unitID {
			return a.DeviceInstance
		}
	}
	return unitID
}

// RegisterRun is a contiguous (or near-contiguous) span of registers within
// one service's map, sized to fit within one or more Modbus read requests.
type RegisterRun struct {
	Start  uint16
	Length int
	Fields []RegisterDef
}

// maxMergeGap controls how large a gap between two fields' addresses is
// still folded into the same run/request. It is 0: a real device (tested
// against a live Cerbo GX) answers "illegal data address" for the RESERVED
// filler registers documented in the source map, not a placeholder value -
// so a single batched read spanning one of those gaps fails outright and
// would take down every field on either side of it with it. Only registers
// that are genuinely back-to-back are ever read together.
const maxMergeGap = 0

// maxChunk is the largest register count requested in a single Modbus
// ReadHoldingRegisters call. The protocol technically allows up to 125; 100
// is used here to stay comfortably inside what real gateways accept.
const maxChunk = 100

// BuildRuns groups a service's sorted fields into contiguous runs.
func BuildRuns(defs []RegisterDef) []RegisterRun {
	var runs []RegisterRun
	for _, d := range defs {
		end := int(d.Register) + d.Length
		if len(runs) > 0 {
			last := &runs[len(runs)-1]
			runEnd := int(last.Start) + last.Length
			if int(d.Register)-runEnd <= maxMergeGap {
				if end > runEnd {
					last.Length = end - int(last.Start)
				}
				last.Fields = append(last.Fields, d)
				continue
			}
		}
		runs = append(runs, RegisterRun{Start: d.Register, Length: end - int(d.Register), Fields: []RegisterDef{d}})
	}
	return runs
}
