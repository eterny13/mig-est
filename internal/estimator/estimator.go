// Package estimator produces time and disk ranges. It deliberately reports a
// range and confidence instead of pretending to predict an exact duration.
package estimator

import (
	"fmt"
	"math"
	"strings"

	"github.com/eterny13/mig-est/internal/catalog"
	"github.com/eterny13/mig-est/internal/history"
	"github.com/eterny13/mig-est/internal/model"
)

const (
	mb = 1024 * 1024

	copyMinMBps = 50.0
	copyMaxMBps = 150.0

	buildMinMBps = 25.0
	buildMaxMBps = 100.0

	scanMinMBps = 100.0
	scanMaxMBps = 300.0

	fulltextMinMBps = 15.0
	fulltextMaxMBps = 50.0
)

// Estimator computes ranges for a fixed environment.
type Estimator struct {
	thresholds model.Thresholds
	records    []history.Record
	host       string
	database   string
}

// New creates an estimator. records are calibration records loaded from
// history.jsonl; host and database scope calibration to the current instance.
func New(th model.Thresholds, records []history.Record, host, database string) *Estimator {
	if th.DiskSafetyFactor < 1 {
		th.DiskSafetyFactor = 1.5
	}
	return &Estimator{thresholds: th, records: records, host: host, database: database}
}

// Estimate returns a time range and the disk required in MB. A nil disk value
// means the table size is unknown.
func (e *Estimator) Estimate(stmt *model.Statement, table *catalog.TableInfo) (model.Estimate, *float64) {
	alg := stmt.PredictedAlgorithm
	size := int64(0)
	if table != nil {
		size = table.SizeBytes()
	}
	if alg == model.AlgorithmInstant || stmt.Kind == model.KindCreateTable || stmt.Kind == model.KindRenameTable {
		disk := 0.0
		return model.Estimate{
			MinSeconds: floatPtr(0),
			MaxSeconds: floatPtr(1),
			Confidence: "high",
			Basis:      "metadata-only operation; excludes metadata lock waiting",
		}, &disk
	}
	if alg == model.AlgorithmUnknown && stmt.Kind == model.KindUnsupported {
		return model.Estimate{
			Confidence: "low",
			Basis:      "unsupported statement; no estimate",
		}, nil
	}
	if size <= 0 {
		return model.Estimate{
			Confidence: "low",
			Basis:      "table size is unavailable; run online or pass --table-size",
		}, nil
	}

	switch alg {
	case model.AlgorithmCopy:
		return e.copyEstimate(stmt, table, size)
	case model.AlgorithmInplace:
		return e.inplaceEstimate(stmt, table, size)
	default:
		return model.Estimate{
			Confidence: "low",
			Basis:      "algorithm is unknown; no numeric estimate",
		}, nil
	}
}

func (e *Estimator) copyEstimate(stmt *model.Statement, table *catalog.TableInfo, size int64) (model.Estimate, *float64) {
	speedMin, speedMax := copyMinMBps, copyMaxMBps
	records, applied, median := e.calibration(table, model.AlgorithmCopy)
	basis := fmt.Sprintf("table size %.1f MB; assumed copy speed %.0f-%.0f MB/s", float64(size)/mb, speedMin, speedMax)
	confidence := "low"
	if applied {
		speedMin = median * 0.8
		speedMax = median * 1.25
		basis = fmt.Sprintf("table size %.1f MB; calibrated copy speed %.1f MB/s (median of %d records)", float64(size)/mb, median, records)
		confidence = "medium"
		if records >= 10 {
			confidence = "high"
		}
	}
	est := model.Estimate{
		MinSeconds:         floatPtr(float64(size) / mb / speedMax),
		MaxSeconds:         floatPtr(float64(size) / mb / speedMin),
		Confidence:         confidence,
		Basis:              basis,
		CalibrationRecords: records,
		CalibrationMBps:    median,
		Calibrated:         applied,
	}
	disk := float64(size) / mb * e.thresholds.DiskSafetyFactor
	return est, &disk
}

func (e *Estimator) inplaceEstimate(stmt *model.Statement, table *catalog.TableInfo, size int64) (model.Estimate, *float64) {
	op := stmt.Operation
	needBuild := false
	needScan := false
	fulltext := false
	switch op {
	case model.OpAddIndex, model.OpAddPrimaryKey:
		needBuild = true
		if len(stmt.Operations) > 0 {
			for _, o := range stmt.Operations {
				if strings.EqualFold(o.IndexKind, "FULLTEXT") {
					fulltext = true
				}
			}
		}
	case model.OpModifyColumn, model.OpChangeColumn, model.OpAddColumn, model.OpDropColumn:
		needBuild = true
	case model.OpAddForeignKey, model.OpAddCheck:
		needScan = true
	}
	if !needBuild && !needScan {
		disk := 0.0
		return model.Estimate{
			MinSeconds: floatPtr(0),
			MaxSeconds: floatPtr(2),
			Confidence: "high",
			Basis:      "in-place metadata change; excludes metadata lock waiting",
		}, &disk
	}

	var lowSpeed, highSpeed float64
	var workBytes int64
	var workLabel string
	var disk *float64
	switch {
	case needScan:
		lowSpeed, highSpeed = scanMinMBps, scanMaxMBps
		workBytes = size
		workLabel = "full-table validation scan"
	case fulltext:
		lowSpeed, highSpeed = fulltextMinMBps, fulltextMaxMBps
		workBytes = size
		workLabel = "FULLTEXT index build"
	default:
		lowSpeed, highSpeed = buildMinMBps, buildMaxMBps
		workBytes = indexSize(table)
		if stmt.Operation == model.OpModifyColumn || stmt.Operation == model.OpChangeColumn || stmt.Operation == model.OpAddColumn || stmt.Operation == model.OpDropColumn {
			workBytes = size
		}
		workLabel = "in-place rebuild"
		d := float64(workBytes) / mb * 1.3
		disk = &d
	}
	if needScan {
		d := 0.0
		disk = &d
	}
	records, applied, median := e.calibration(table, model.AlgorithmInplace)
	confidence := "low"
	basis := fmt.Sprintf("%s, %.1f MB of work; assumed %.0f-%.0f MB/s", workLabel, float64(workBytes)/mb, lowSpeed, highSpeed)
	if applied {
		lowSpeed = median * 0.8
		highSpeed = median * 1.25
		basis = fmt.Sprintf("%s, %.1f MB of work; calibrated speed %.1f MB/s (median of %d records)", workLabel, float64(workBytes)/mb, median, records)
		confidence = "medium"
		if records >= 10 {
			confidence = "high"
		}
	}
	return model.Estimate{
		MinSeconds:         floatPtr(float64(workBytes) / mb / highSpeed),
		MaxSeconds:         floatPtr(float64(workBytes) / mb / lowSpeed),
		Confidence:         confidence,
		Basis:              basis,
		CalibrationRecords: records,
		CalibrationMBps:    median,
		Calibrated:         applied,
	}, disk
}

func (e *Estimator) calibration(table *catalog.TableInfo, alg model.Algorithm) (records int, applied bool, median float64) {
	if table == nil || len(e.records) == 0 {
		return 0, false, 0
	}
	n, ok, med := countCalibration(e.records, table.Name, string(alg), e.host, e.database)
	return n, ok, med
}

func countCalibration(records []history.Record, table, alg, host, db string) (int, bool, float64) {
	med, n, applied := history.Calibrate(records, table, alg, host, db, 10)
	return n, applied, med
}

func indexSize(table *catalog.TableInfo) int64 {
	if table == nil {
		return 0
	}
	if table.IndexLength > 0 && len(table.Indexes) > 0 {
		avg := table.IndexLength / int64(len(table.Indexes))
		if avg < 1*mb {
			avg = 1 * mb
		}
		return avg
	}
	if table.Rows > 0 {
		n := table.Rows * 32
		if n < 1*mb {
			n = 1 * mb
		}
		return n
	}
	return table.IndexLength
}

func floatPtr(v float64) *float64 {
	if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	v = math.Round(v*10) / 10
	return &v
}
