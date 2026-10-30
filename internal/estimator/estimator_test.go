package estimator

import (
	"math"
	"testing"

	"github.com/eterny13/mig-est/internal/catalog"
	"github.com/eterny13/mig-est/internal/history"
	"github.com/eterny13/mig-est/internal/model"
)

func approx(got, want float64) bool { return math.Abs(got-want) < 0.11 }

func TestCopyEstimateRangeAndDisk(t *testing.T) {
	table := &catalog.TableInfo{Name: "orders", DataLength: 800 * mb, IndexLength: 200 * mb}
	stmt := &model.Statement{PredictedAlgorithm: model.AlgorithmCopy, Kind: model.KindAlterTable}
	est := New(model.Thresholds{DiskSafetyFactor: 1.5}, nil, "", "")
	got, disk := est.Estimate(stmt, table)
	if !got.Available() {
		t.Fatal("estimate should be numeric")
	}
	if !approx(*got.MinSeconds, 1000.0/150.0) || !approx(*got.MaxSeconds, 1000.0/50.0) {
		t.Errorf("range = %v..%v, want ~6.7..20", *got.MinSeconds, *got.MaxSeconds)
	}
	if got.Confidence != "low" || got.Calibrated {
		t.Errorf("confidence/calibrated = %s/%v, want low/false", got.Confidence, got.Calibrated)
	}
	if disk == nil || !approx(*disk, 1500) {
		t.Errorf("disk = %v, want 1500", disk)
	}
}

func TestCalibrationAppliesMedian(t *testing.T) {
	table := &catalog.TableInfo{Name: "orders", DataLength: 1000 * mb}
	stmt := &model.Statement{PredictedAlgorithm: model.AlgorithmCopy, Kind: model.KindAlterTable}
	records := []history.Record{
		{Table: "orders", Algorithm: "COPY", SizeMB: 1000, ActualSeconds: 100, Host: "db:3306", Database: "app"},
		{Table: "orders", Algorithm: "COPY", SizeMB: 1000, ActualSeconds: 120, Host: "db:3306", Database: "app"},
		{Table: "orders", Algorithm: "COPY", SizeMB: 1000, ActualSeconds: 80, Host: "db:3306", Database: "app"},
	}
	est := New(model.Thresholds{DiskSafetyFactor: 1.5}, records, "db:3306", "app")
	got, _ := est.Estimate(stmt, table)
	if !got.Calibrated || got.Confidence != "medium" || got.CalibrationRecords != 3 {
		t.Errorf("calibration = %+v, want applied/medium/3", got)
	}
	// Median speed is 10 MB/s: 1000 MB / 12.5 = 80s, / 8 = 125s.
	if !approx(*got.MinSeconds, 80) || !approx(*got.MaxSeconds, 125) {
		t.Errorf("calibrated range = %v..%v, want ~80..125", *got.MinSeconds, *got.MaxSeconds)
	}
}

func TestCalibrationRequiresThreeRecords(t *testing.T) {
	table := &catalog.TableInfo{Name: "orders", DataLength: 1000 * mb}
	stmt := &model.Statement{PredictedAlgorithm: model.AlgorithmCopy, Kind: model.KindAlterTable}
	records := []history.Record{
		{Table: "orders", Algorithm: "COPY", SizeMB: 1000, ActualSeconds: 100},
		{Table: "orders", Algorithm: "COPY", SizeMB: 1000, ActualSeconds: 120},
	}
	got, _ := New(model.Thresholds{DiskSafetyFactor: 1.5}, records, "", "").Estimate(stmt, table)
	if got.Calibrated || got.Confidence != "low" {
		t.Errorf("two records should not calibrate: %+v", got)
	}
}

func TestInstantAndUnknown(t *testing.T) {
	est := New(model.Thresholds{DiskSafetyFactor: 1.5}, nil, "", "")
	instant, disk := est.Estimate(&model.Statement{PredictedAlgorithm: model.AlgorithmInstant, Kind: model.KindAlterTable}, &catalog.TableInfo{DataLength: mb})
	if !instant.Available() || *instant.MaxSeconds > 1 || disk == nil || *disk != 0 {
		t.Errorf("instant = %+v disk=%v", instant, disk)
	}
	unknown, disk := est.Estimate(&model.Statement{PredictedAlgorithm: model.AlgorithmUnknown, Kind: model.KindAlterTable}, &catalog.TableInfo{DataLength: mb})
	if unknown.Available() || disk != nil {
		t.Errorf("unknown = %+v disk=%v, want no estimate", unknown, disk)
	}
}
