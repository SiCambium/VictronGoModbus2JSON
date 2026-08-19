package main

// FieldValue is the decoded, JSON-ready result of one register field.
type FieldValue struct {
	Path      string      `json:"path"`
	Register  uint16      `json:"register"`
	DataType  string      `json:"dataType"`
	Access    string      `json:"access"`
	Unit      string      `json:"unit,omitempty"`
	Raw       interface{} `json:"raw"`
	Value     interface{} `json:"value,omitempty"`
	Label     string      `json:"label,omitempty"` // enum text for the current raw value, if known
	Available bool        `json:"available"`
	Note      string      `json:"note,omitempty"`
}

// decodeField turns the raw holding-register words for one field (already
// sliced to def.Length words, taken from a run/chunk read at def.Register)
// into a FieldValue. Victron marks a property "not currently available"
// using the maximum representable value for the field's width/signedness
// (0xFFFF / 0x7FFF / 0xFFFFFFFF / 0x7FFFFFFF) rather than a Modbus
// exception; that convention is applied here so absent sensors read as
// null instead of a nonsense number.
func decodeField(def RegisterDef, words []uint16) FieldValue {
	fv := FieldValue{
		Path:      def.Path,
		Register:  def.Register,
		DataType:  dataTypeLabel(def),
		Access:    def.Access,
		Unit:      def.Unit,
		Available: true,
	}

	switch def.Kind {
	case "uint16":
		raw := words[0]
		fv.Raw = raw
		if raw == 0xFFFF {
			fv.Available = false
			return fv
		}
		fv.Value, fv.Label = scaledOrLabeled(def, float64(raw), int(raw))

	case "int16":
		raw := int16(words[0])
		fv.Raw = raw
		if raw == 0x7FFF {
			fv.Available = false
			return fv
		}
		fv.Value, fv.Label = scaledOrLabeled(def, float64(raw), int(raw))

	case "uint32":
		raw := uint32(words[0])<<16 | uint32(words[1])
		fv.Raw = raw
		if raw == 0xFFFFFFFF {
			fv.Available = false
			return fv
		}
		fv.Value, fv.Label = scaledOrLabeled(def, float64(raw), int(raw))

	case "int32":
		raw := int32(uint32(words[0])<<16 | uint32(words[1]))
		fv.Raw = raw
		if raw == 0x7FFFFFFF {
			fv.Available = false
			return fv
		}
		fv.Value, fv.Label = scaledOrLabeled(def, float64(raw), int(raw))

	case "string":
		fv.Raw = words
		fv.Value = decodeModbusString(words)

	case "internal":
		fv.Raw = words
		fv.Value = nil
		fv.Note = "Victron-internal register format, not documented in the public register map; raw words only"

	default:
		fv.Raw = words
		fv.Note = "unrecognized register kind: " + def.Kind
	}

	return fv
}

func dataTypeLabel(def RegisterDef) string {
	switch def.Kind {
	case "uint16", "int16", "uint32", "int32", "internal":
		return def.Kind
	case "string":
		return "string"
	default:
		return def.Kind
	}
}

// scaledOrLabeled applies the field's enum lookup if one exists, otherwise
// applies its scale factor (value = raw / scale) to produce a physical
// value in the field's declared unit.
func scaledOrLabeled(def RegisterDef, raw float64, rawInt int) (value interface{}, label string) {
	if def.Enum != nil {
		if l, ok := def.Enum[rawInt]; ok {
			label = l
		}
		return rawInt, label
	}
	if def.Scale == 1 {
		return rawInt, ""
	}
	return raw / def.Scale, ""
}

// decodeModbusString decodes Victron's 2-ASCII-characters-per-register
// packing (high byte first), trimming the null-byte padding.
func decodeModbusString(words []uint16) string {
	b := make([]byte, 0, len(words)*2)
	for _, w := range words {
		hi := byte(w >> 8)
		lo := byte(w & 0xFF)
		if hi == 0 {
			break
		}
		b = append(b, hi)
		if lo == 0 {
			break
		}
		b = append(b, lo)
	}
	return string(b)
}
