package main

import "testing"

func TestLoadRegisterMap(t *testing.T) {
	byService, err := LoadRegisterMap()
	if err != nil {
		t.Fatalf("LoadRegisterMap: %v", err)
	}
	if len(byService) < 30 {
		t.Fatalf("expected ~32 service classes, got %d", len(byService))
	}
	vebus, ok := byService["com.victronenergy.vebus"]
	if !ok || len(vebus) == 0 {
		t.Fatalf("expected com.victronenergy.vebus to have fields")
	}
	if vebus[0].Register != 3 {
		t.Fatalf("expected vebus's first register to be 3, got %d", vebus[0].Register)
	}
}

// From the official GX Modbus-TCP manual worked example: requesting VE.Bus
// input L1 voltage (unit ID 246, register 3) returns raw 2302, i.e. 230.2 V
// once the documented /10 scale factor is applied.
func TestDecodeVEBusVoltageExample(t *testing.T) {
	byService, err := LoadRegisterMap()
	if err != nil {
		t.Fatalf("LoadRegisterMap: %v", err)
	}
	var def RegisterDef
	for _, d := range byService["com.victronenergy.vebus"] {
		if d.Path == "/Ac/ActiveIn/L1/V" {
			def = d
		}
	}
	if def.Path == "" {
		t.Fatal("could not find /Ac/ActiveIn/L1/V in vebus map")
	}
	fv := decodeField(def, []uint16{2302})
	v, ok := fv.Value.(float64)
	if !ok || v != 230.2 {
		t.Fatalf("expected 230.2, got %#v (available=%v)", fv.Value, fv.Available)
	}
}

func TestDecodeNotAvailableSentinels(t *testing.T) {
	u16 := RegisterDef{Kind: "uint16", Scale: 1}
	if fv := decodeField(u16, []uint16{0xFFFF}); fv.Available {
		t.Fatalf("expected uint16 0xFFFF to be marked unavailable")
	}
	i16 := RegisterDef{Kind: "int16", Scale: 1}
	if fv := decodeField(i16, []uint16{0x7FFF}); fv.Available {
		t.Fatalf("expected int16 0x7FFF to be marked unavailable")
	}
	i32 := RegisterDef{Kind: "int32", Scale: 1}
	if fv := decodeField(i32, []uint16{0x7FFF, 0xFFFF}); fv.Available {
		t.Fatalf("expected int32 0x7FFFFFFF to be marked unavailable")
	}
}

func TestDecodeString(t *testing.T) {
	def := RegisterDef{Kind: "string", Length: 3}
	// "HQ" then "12" then a trailing null pad -> "HQ12"
	words := []uint16{'H'<<8 | 'Q', '1'<<8 | '2', 0}
	fv := decodeField(def, words)
	if fv.Value != "HQ12" {
		t.Fatalf("expected HQ12, got %#v", fv.Value)
	}
}

func TestBuildRunsOnlyMergesBackToBackFields(t *testing.T) {
	// A live Cerbo GX answers "illegal data address" for the RESERVED filler
	// registers in the source map (verified against 172.22.25.100), so even
	// a small gap must start a new run - merging through it would fail the
	// whole batched read and lose every field on both sides of the gap.
	defs := []RegisterDef{
		{Register: 100, Length: 1},
		{Register: 101, Length: 2}, // contiguous -> merge
		{Register: 105, Length: 1}, // gap 2 -> new run, even though small
		{Register: 200, Length: 1}, // gap 94 -> new run
	}
	runs := BuildRuns(defs)
	if len(runs) != 3 {
		t.Fatalf("expected 3 runs, got %d: %+v", len(runs), runs)
	}
	if runs[0].Start != 100 || runs[0].Length != 3 {
		t.Fatalf("unexpected first run: %+v", runs[0])
	}
	if runs[1].Start != 105 || runs[1].Length != 1 {
		t.Fatalf("unexpected second run: %+v", runs[1])
	}
	if runs[2].Start != 200 || runs[2].Length != 1 {
		t.Fatalf("unexpected third run: %+v", runs[2])
	}
}

func TestUnitIDAliasesAndDeviceInstance(t *testing.T) {
	aliases, err := LoadUnitIDAliases()
	if err != nil {
		t.Fatalf("LoadUnitIDAliases: %v", err)
	}
	if len(aliases) == 0 {
		t.Fatal("expected at least one alias entry")
	}
	if di := deviceInstanceForUnitID(100, aliases); di != 0 {
		t.Fatalf("expected unit 100 -> device instance 0, got %d", di)
	}
	if di := deviceInstanceForUnitID(246, aliases); di != 257 {
		t.Fatalf("expected legacy unit 246 -> device instance 257, got %d", di)
	}
	if di := deviceInstanceForUnitID(30, aliases); di != 30 {
		t.Fatalf("expected general-rule unit 30 -> device instance 30, got %d", di)
	}
}
