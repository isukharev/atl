package plugincontract

import (
	"errors"
	"strings"
	"testing"
)

func TestEvaluateStartupMatrix(t *testing.T) {
	compatible := Markers{InterfaceContracts: []string{"1"}, ProductVersions: []string{"1.2.3"}}
	for _, test := range []struct {
		name      string
		markers   Markers
		binary    string
		want      StartupStatus
		wantError error
	}{
		{
			name:   "unmarked standalone or legacy plugin",
			binary: "1.2.3",
			want: StartupStatus{
				InterfaceContract: InterfaceUnverified,
				ProductVersion:    ProductUnverified,
			},
		},
		{
			name:    "compatible and product match",
			markers: compatible,
			binary:  "1.2.3",
			want: StartupStatus{
				InterfaceContract: InterfaceCompatible,
				ProductVersion:    ProductMatch,
			},
		},
		{
			name:    "compatible and product mismatch",
			markers: compatible,
			binary:  "9.8.7",
			want: StartupStatus{
				InterfaceContract: InterfaceCompatible,
				ProductVersion:    ProductMismatch,
			},
		},
		{
			name: "incompatible interface",
			markers: Markers{
				InterfaceContracts: []string{"2"},
				ProductVersions:    []string{"1.2.3"},
			},
			binary:    "1.2.3",
			wantError: ErrIncompatibleInterface,
		},
		{
			name: "missing product marker",
			markers: Markers{
				InterfaceContracts: []string{"1"},
			},
			binary:    "1.2.3",
			wantError: ErrIncompleteMarkers,
		},
		{
			name: "repeated interface marker",
			markers: Markers{
				InterfaceContracts: []string{"1", "1"},
				ProductVersions:    []string{"1.2.3"},
			},
			binary:    "1.2.3",
			wantError: ErrRepeatedMarkers,
		},
		{
			name: "invalid product marker",
			markers: Markers{
				InterfaceContracts: []string{"1"},
				ProductVersions:    []string{"private value"},
			},
			binary:    "1.2.3",
			wantError: ErrInvalidProductVersion,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := Evaluate(test.markers, test.binary)
			if !errors.Is(err, test.wantError) {
				t.Fatalf("error=%v, want %v", err, test.wantError)
			}
			if err == nil && got != test.want {
				t.Fatalf("status=%+v, want %+v", got, test.want)
			}
			if err != nil && strings.Contains(err.Error(), "private value") {
				t.Fatalf("validation error disclosed marker value: %v", err)
			}
		})
	}
}

func TestValidProductVersionIsBoundedOpaqueToken(t *testing.T) {
	for _, value := range []string{"0.7.1", "1.2.3-rc.1+build", "dev"} {
		if !ValidProductVersion(value) {
			t.Errorf("ValidProductVersion(%q)=false", value)
		}
	}
	for _, value := range []string{"", " 1.2.3", "1.2.3 ", ".1.2.3", "private/value", strings.Repeat("a", 129)} {
		if ValidProductVersion(value) {
			t.Errorf("ValidProductVersion(%q)=true", value)
		}
	}
}
