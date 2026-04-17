package engine

import (
	"bytes"

	"github.com/anatol/smart.go"
)

type SmartInfo struct {
	Model          string `json:"model"`
	Serial         string `json:"serial"`
	Temperature    int    `json:"temperature"`
	PowerOnHours   uint64 `json:"power_on_hours"`
	PowerCycles    uint64 `json:"power_cycles"`
	LoadCycleCount uint64 `json:"load_cycle_count"`
	DataMetric     string `json:"data_metric"`
	DataValue      uint64 `json:"data_value"`
	IsFailing      bool   `json:"is_failing"`
	Status         string `json:"status"`
	Protocol       string `json:"protocol"`
	RotationRate   uint16 `json:"rotation_rate"`
}

type SmartAttribute struct {
	ID        uint8  `json:"id"`
	Name      string `json:"name"`
	Value     uint8  `json:"value"`
	Worst     uint8  `json:"worst"`
	Threshold uint8  `json:"threshold"`
	RawValue  uint64 `json:"raw_value"`
	Status    string `json:"status"`
	Flags     string `json:"flags"`
}

type FullSmartReport struct {
	Info       SmartInfo           `json:"info"`
	Attributes []SmartAttribute    `json:"attributes,omitempty"`
	NVMeLog    *smart.NvmeSMARTLog `json:"nvme_log,omitempty"`
}

func readSmartInfoNVMe(d *smart.NVMeDevice) (*SmartInfo, *smart.NvmeSMARTLog) {
	info := &SmartInfo{Status: "UNKNOWN", Protocol: "NVMe", RotationRate: 0}
	c, _, err := d.Identify()
	if err == nil {
		info.Model = c.ModelNumber()
		info.Serial = c.SerialNumber()
	}
	sm, err := d.ReadSMART()
	if err == nil {
		info.Temperature = int(sm.Temperature) - 273
		if info.Temperature < 0 {
			info.Temperature = 0
		}
		info.PowerOnHours = sm.PowerOnHours.Val[0]
		info.PowerCycles = sm.PowerCycles.Val[0]
		info.DataMetric = "data_written"
		info.DataValue = (sm.DataUnitsWritten.Val[0] * 1000) / (2 * 1024 * 1024)
		if sm.CritWarning > 0 {
			info.IsFailing = true
			info.Status = "FAILED"
		} else {
			info.Status = "PASSED"
		}
	}
	return info, sm
}

func readSmartInfoSATA(d *smart.SataDevice) *SmartInfo {
	info := &SmartInfo{Status: "UNKNOWN", Protocol: "SATA"}
	id, err := d.Identify()
	if err == nil {
		info.Model = id.ModelNumber()
		info.Serial = id.SerialNumber()
		info.RotationRate = id.RotationRate
	}
	data, err := d.ReadSMARTData()
	if err == nil {
		if attr, ok := data.Attrs[194]; ok {
			temp, _, _, _, _ := attr.ParseAsTemperature()
			info.Temperature = int(temp)
		} else if attr, ok := data.Attrs[190]; ok {
			temp, _, _, _, _ := attr.ParseAsTemperature()
			info.Temperature = int(temp)
		}
		if attr, ok := data.Attrs[9]; ok {
			info.PowerOnHours = attr.ValueRaw
		}
		if attr, ok := data.Attrs[12]; ok {
			info.PowerCycles = attr.ValueRaw
		}
		if attr, ok := data.Attrs[193]; ok {
			info.LoadCycleCount = attr.ValueRaw
		}
		if attr, ok := data.Attrs[241]; ok && attr.ValueRaw > 0 {
			info.DataMetric = "data_written"
			info.DataValue = attr.ValueRaw * 512 / (1024 * 1024 * 1024)
		} else if attr, ok := data.Attrs[5]; ok {
			info.DataMetric = "reallocated_sectors"
			info.DataValue = attr.ValueRaw
		}
		info.Status = "PASSED"
		thresholds, err := d.ReadSMARTThresholds()
		if err == nil {
			for id, attr := range data.Attrs {
				if thresh, ok := thresholds.Thresholds[id]; ok && thresh > 0 {
					if attr.Current <= thresh {
						info.IsFailing = true
						info.Status = "FAILED"
						break
					}
				}
			}
		}
	}
	return info
}

func readSmartInfoSCSI(d *smart.ScsiDevice) *SmartInfo {
	info := &SmartInfo{Status: "UNKNOWN", Protocol: "SCSI"}

	serial, _ := d.SerialNumber()
	info.Serial = serial

	inq, err := d.Inquiry()
	if err == nil {
		info.Model = string(bytes.TrimSpace(inq.ProductIdent[:]))
	}

	health, err := d.ReadHealth()
	if err == nil {
		info.Temperature = health.Temperature
		info.PowerOnHours = health.PowerOnHours
		info.IsFailing = health.IsFailing
		info.Status = health.StatusString
		if health.Defects > 0 {
			info.DataMetric = "grown_defects"
			info.DataValue = health.Defects
		}
	}

	return info
}

var GetSmartInfo = func(devicePath string) (*SmartInfo, error) {
	dev, err := smart.Open(devicePath)
	if err != nil {
		return nil, err
	}
	defer dev.Close()

	switch d := dev.(type) {
	case *smart.NVMeDevice:
		info, _ := readSmartInfoNVMe(d)
		return info, nil
	case *smart.SataDevice:
		return readSmartInfoSATA(d), nil
	case *smart.ScsiDevice:
		return readSmartInfoSCSI(d), nil
	}
	return &SmartInfo{Status: "UNKNOWN"}, nil
}

func GetFullSmartReport(devicePath string) (*FullSmartReport, error) {
	dev, err := smart.Open(devicePath)
	if err != nil {
		return nil, err
	}
	defer dev.Close()

	report := &FullSmartReport{}

	switch d := dev.(type) {
	case *smart.NVMeDevice:
		info, smLog := readSmartInfoNVMe(d)
		report.Info = *info
		report.NVMeLog = smLog

	case *smart.SataDevice:
		report.Info = *readSmartInfoSATA(d)
		data, err := d.ReadSMARTData()
		if err == nil {
			thresholds, _ := d.ReadSMARTThresholds()
			for _, attr := range data.Attrs {
				sAttr := SmartAttribute{
					ID:       attr.Id,
					Name:     attr.Name,
					Value:    attr.Current,
					Worst:    attr.Worst,
					RawValue: attr.ValueRaw,
					Status:   "PASSED",
				}
				if thresholds != nil {
					if thresh, ok := thresholds.Thresholds[attr.Id]; ok {
						sAttr.Threshold = thresh
						if thresh > 0 && attr.Current <= thresh {
							sAttr.Status = "FAILED"
						}
					}
				}
				if attr.Flags&0x01 != 0 {
					sAttr.Flags += "Pre-fail "
				} else {
					sAttr.Flags += "Old-age "
				}
				if attr.Flags&0x02 != 0 {
					sAttr.Flags += "Online"
				}
				report.Attributes = append(report.Attributes, sAttr)
			}
		}

	case *smart.ScsiDevice:
		report.Info = *readSmartInfoSCSI(d)
	}

	return report, nil
}
