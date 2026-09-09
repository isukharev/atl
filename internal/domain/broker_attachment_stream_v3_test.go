package domain

import (
	"reflect"
	"strings"
	"testing"
)

func TestBrokerAttachmentV3DomainSurfaceHasNoURLTunnel(t *testing.T) {
	for _, value := range []any{BrokerAttachmentArgumentsV3{}, BrokerJiraAttachmentSnapshotV3{}, BrokerAttachmentRequestV3{}, BrokerAttachmentStreamAnchorV3{}} {
		typeOf := reflect.TypeOf(value)
		for index := range typeOf.NumField() {
			name := strings.ToLower(typeOf.Field(index).Name)
			if strings.Contains(name, "url") || strings.Contains(name, "path") || strings.Contains(name, "header") || strings.Contains(name, "method") {
				t.Fatalf("%s exposes transport selector %s", typeOf.Name(), typeOf.Field(index).Name)
			}
		}
	}
	handle := reflect.TypeOf((*BrokerJiraAttachmentOpenHandleV3)(nil)).Elem()
	port := reflect.TypeOf((*BrokerJiraAttachmentPortV3)(nil)).Elem()
	if handle.NumMethod() != 3 || port.NumMethod() != 2 {
		t.Fatalf("handle methods=%d port methods=%d", handle.NumMethod(), port.NumMethod())
	}
	for _, name := range []string{"Close", "Open", "Snapshot"} {
		if _, ok := handle.MethodByName(name); !ok {
			t.Fatalf("handle method %s missing", name)
		}
	}
}
