//go:build windows

package integration

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestTask20UnknownObjectTypeFailsClosed(t *testing.T) {
	if _, ok := task20KindForObjectType("FutureRuntimeObject"); ok {
		t.Fatal("unknown object type was classified")
	}
}

func TestTask20ClassifySemaphoreAndSectionKeepsLeakAccounting(t *testing.T) {
	for _, objectType := range []string{"Semaphore", "Section"} {
		t.Run(objectType, func(t *testing.T) {
			identity := task20HandleIdentity{Value: 1, Object: 2}
			resource, err := task20ClassifyHandleWith(1, identity, task20HandleClassificationAPI{
				queryObjectType: func(windows.Handle) (string, error) { return objectType, nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			owned, err := resource.applicationOwned()
			if err != nil || !owned || resource.Identity != identity || resource.Type != objectType || task20HandleKindName(resource.Kind) != objectType {
				t.Fatalf("resource=%+v owned=%t err=%v; want exact handle retained for leak accounting", resource, owned, err)
			}
			handles, err := task20ApplicationHandles(map[task20HandleIdentity]task20ResourceIdentity{identity: resource})
			if err != nil || handles[identity] != resource {
				t.Fatalf("application handles=%+v err=%v; want known handle retained", handles, err)
			}
		})
	}
}
