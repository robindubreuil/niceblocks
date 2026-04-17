# USB-to-SATA SMART Fix

## Problem
SATA drives connected over USB were not reporting SMART data in niceblocks. The smart.go library was detecting these drives as generic SCSI devices (which don't support SMART) instead of SATA devices.

## Root Cause
The smart.go library's `OpenSata()` function only opened devices that reported `"ATA     "` in the SCSI INQUIRY VendorIdent field. However, most USB-to-SATA bridges report the actual drive manufacturer (e.g., `"TOSHIBA "`) instead of `"ATA     "`.

## Solution
Modified `sata_linux.go` to be more robust:
- Still fast-path for standard `"ATA     "` identifiers
- For other direct-access devices, actually **test** if they respond to ATA IDENTIFY command
- If ATA IDENTIFY succeeds → treat as SATA device
- If it fails → return error (falls back to pure SCSI)

## Implementation
The fix is implemented as a **local fork** to avoid losing changes on dependency updates:

```
local/smart.go/          # Local fork with the fix
├── go.mod              # Module definition
├── README.md           # Documentation of changes
└── sata_linux.go       # Modified file with the fix
```

The main project uses this via `go.mod`:
```
replace github.com/anatol/smart.go => ./local/smart.go
```

## Verification
Test shows the drive is now properly detected:
```
Device type: *smart.SataDevice
Device detected as: SATA
Model: TOSHIBA MQ04ABF100
Temperature: 22°C
Power_On_Hours: 183
Power_Cycle_Count: 162
```

## Next Steps
When ready, push the local fork to GitHub and submit a PR upstream:
```bash
cd local/smart.go
git init
git add .
git commit -m "Fix SATA detection for USB bridges"
git remote add origin git@github.com:YOUR_USERNAME/smart.go.git
git push -u origin main
```

Then submit PR to: https://github.com/anatol/smart.go
