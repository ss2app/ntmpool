package coininstance

import "testing"

func TestResolveChainAudit(t *testing.T) {
	trueValue, falseValue := true, false
	tests := []struct {
		name       string
		configured *bool
		persistent bool
		wantNil    bool
		want       bool
	}{
		{name: "mem auto", wantNil: true},
		{name: "PG auto", persistent: true, want: true},
		{name: "mem explicit true", configured: &trueValue, want: true},
		{name: "PG explicit false", configured: &falseValue, persistent: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveChainAudit(tt.configured, tt.persistent)
			if tt.wantNil {
				if got != nil {
					t.Fatalf("got=%v want=nil", *got)
				}
				return
			}
			if got == nil || *got != tt.want {
				t.Fatalf("got=%v want=%v", got, tt.want)
			}
		})
	}
}
