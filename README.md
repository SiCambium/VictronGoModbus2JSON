# victron-modbus-json

Connects to a Victron Energy GX device (Cerbo GX, Venus GX, CCGX, ...) over
Modbus-TCP, discovers every connected device/service, and dumps all of their
documented registers as a single JSON document.

## How it works

- `data/attributes.csv` and `data/unitid2di.csv` are Victron's own published
  Modbus-TCP register map, taken verbatim from
  https://github.com/victronenergy/dbus_modbustcp. They cover all 32 device
  classes Victron documents (battery, solarcharger, vebus, pvinverter, grid
  meter, tank/temperature/GPS senders, generator, EV charger, ...).
- A Modbus **unit ID** addresses one D-Bus **device instance** directly
  (unit ID == device instance for 0-247; unit 100 is an alias for instance 0,
  the always-present system/settings/platform services). The tool scans a
  unit ID range (default 0-247) and, for each one, tries the first register
  of every known device class; a class only "matches" if the device behind
  that unit ID answers without a Modbus exception.
- Each matched device then has every register in its class read back
  (batched into as few requests as the address layout safely allows) and
  decoded using the map's declared data type, scale factor, and enum table.
- A register whose raw value is Victron's "not currently available"
  sentinel (0xFFFF / 0x7FFF / 0xFFFFFFFF / 0x7FFFFFFF) is reported with
  `"available": false` instead of a misleading number. A register that the
  device's firmware doesn't implement (a genuine Modbus exception, e.g. an
  optional/newer field) is reported with a `note` explaining the read
  failed, rather than being silently dropped.

## Build

```
go build -o victron-modbus-json .
```

## Run

```
./victron-modbus-json -host 172.22.25.100 -out victron.json
```

Progress and the list of discovered devices are printed to stderr; the JSON
document goes to stdout (or to `-out <file>`).

Flags:

| Flag         | Default | Meaning                                             |
|--------------|---------|------------------------------------------------------|
| `-host`      | (none)  | GX device IP/hostname (required)                    |
| `-port`      | 502     | Modbus-TCP port                                      |
| `-timeout`   | 800ms   | per-request timeout                                  |
| `-workers`   | 4       | concurrent Modbus-TCP connections used while scanning|
| `-unit-min`  | 0       | lowest unit ID to scan                               |
| `-unit-max`  | 247     | highest unit ID to scan                              |
| `-out`       | stdout  | write JSON to this file instead                      |
| `-pretty`    | true    | pretty-print the JSON                                |

A full 0-247 scan takes well under a minute on a local network.

## Output shape

```jsonc
{
  "host": "172.22.25.100",
  "port": 502,
  "scannedAt": "2026-08-19T10:16:36+01:00",
  "unitIdRange": [0, 247],
  "devices": [
    {
      "unitId": 227,
      "deviceInstance": 257,
      "serviceType": "com.victronenergy.vebus",
      "registers": [
        {
          "path": "/Ac/ActiveIn/L1/V",
          "register": 3,
          "dataType": "uint16",
          "access": "R",
          "unit": "V AC",
          "raw": 2439,
          "value": 243.9,
          "available": true
        }
      ]
    }
  ],
  "warnings": ["..."]
}
```

Note that a handful of D-Bus services (`system`, `settings`, `platform`,
`hub4`, ...) are singletons pinned to device instance 0, so they will all
show up together under unit ID 100 (and again under unit ID 0) - that is
expected, not a duplicate scan artifact.

## Requires

Modbus-TCP must be enabled on the GX device: **Settings → Services →
Modbus TCP**.

## Converting the JSON to Excel

`json_to_excel.py` turns a scan's JSON output into a workbook: an Overview
sheet (host, scan time, warnings), a Summary sheet listing every device with
a link to its sheet, and one sheet per device with every register as a row
(unavailable/note rows are shaded so gaps are easy to spot).

```
pip install -r requirements.txt
python3 json_to_excel.py victron.json -o victron.xlsx
```
