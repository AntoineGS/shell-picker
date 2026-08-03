package candidate

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestInitialBuilderOverlapsCacheLoadAndMergesLocalFirst(t *testing.T) {
	localStarted := make(chan struct{})
	zoxideStarted := make(chan struct{})
	var localOnce, zoxideOnce sync.Once
	same := filepath.Join(os.TempDir(), "shell-picker-zoxide-same")
	one := filepath.Join(os.TempDir(), "shell-picker-zoxide-one")
	two := filepath.Join(os.TempDir(), "shell-picker-zoxide-two")
	cache, _ := newPortableObservedCache(t, "multiple", zoxideFixtureTimeout, func(event process.ProcessEvent) {
		if event.Phase == "start" {
			zoxideOnce.Do(func() { close(zoxideStarted) })
			<-localStarted
		}
	})
	builder := &Builder{Cache: cache, Policy: ZoxideCached,
		enumerate: func(context.Context, protocol.Picker, pathutil.Location, LocalOptions) ([]Record, error) {
			localOnce.Do(func() { close(localStarted) })
			<-zoxideStarted
			return []Record{newRecord(protocol.KindLocal, ".", []byte("/local")), newRecord(protocol.KindLocal, "same", []byte(same))}, nil
		}}
	got, err := builder.Build(context.Background(), testRequest(protocol.PickerCD, true))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"/local", same, one, two}; !reflect.DeepEqual(paths(got.Records), want) {
		t.Fatalf("paths=%q, want %q", paths(got.Records), want)
	}
	if got.Metrics.ZoxideAttempts != 1 || got.Metrics.ZoxideStarts != 1 || got.Metrics.ZoxideExits != 1 ||
		got.Metrics.ZoxideProcesses != 1 || got.Metrics.ZoxideLive != 0 || got.Metrics.ZoxideMaxLive != 1 {
		t.Fatalf("metrics=%+v", got.Metrics)
	}
}
