package winrt

import "testing"

// TestParseGUID_ReadsTheCanonicalForm pins the parser every interface ID in this
// package goes through. A mis-parsed ID does not fail loudly: it is passed to
// RoGetActivationFactory or QueryInterface, which answer E_NOINTERFACE or turn
// the request down, so the layout is checked here instead.
func TestParseGUID_ReadsTheCanonicalForm(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  guid
	}{
		{
			name:  "braced upper case",
			value: "{5BFFA85A-3384-3540-9940-699120D428A8}",
			want: guid{
				data1: 0x5BFFA85A,
				data2: 0x3384,
				data3: 0x3540,
				data4: [8]byte{0x99, 0x40, 0x69, 0x91, 0x20, 0xD4, 0x28, 0xA8},
			},
		},
		{
			name:  "bare lower case",
			value: "00000036-0000-0000-c000-000000000046",
			want: guid{
				data1: 0x00000036,
				data2: 0,
				data3: 0,
				data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46},
			},
		},
		{
			name:  "surrounding space",
			value: "  {905A0FEF-BC53-11DF-8C49-001E4FC686DA} ",
			want: guid{
				data1: 0x905A0FEF,
				data2: 0xBC53,
				data3: 0x11DF,
				data4: [8]byte{0x8C, 0x49, 0x00, 0x1E, 0x4F, 0xC6, 0x86, 0xDA},
			},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := parseGUID(testCase.value)
			if err != nil {
				t.Fatalf("parseGUID(%q): %v", testCase.value, err)
			}

			if got != testCase.want {
				t.Errorf("parseGUID(%q) = %+v, want %+v", testCase.value, got, testCase.want)
			}
		})
	}
}

// TestParseGUID_RefusesMalformed pins that a typo in a table of IDs is reported
// rather than becoming a different ID.
func TestParseGUID_RefusesMalformed(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "empty", value: ""},
		{name: "one group", value: "5BFFA85A338435409940699120D428A8"},
		{name: "four groups", value: "{5BFFA85A-3384-3540-699120D428A8}"},
		{name: "short group", value: "{5BFFA85-3384-3540-9940-699120D428A8}"},
		{name: "long last group", value: "{5BFFA85A-3384-3540-9940-699120D428A8A}"},
		{name: "not hexadecimal", value: "{5BFFA85A-3384-3540-9940-699120D428AZ}"},
		{name: "an unpaired brace", value: "{5BFFA85A-3384-3540-9940-699120D428A8"},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := parseGUID(testCase.value); err == nil {
				t.Errorf("parseGUID(%q) accepted a malformed ID", testCase.value)
			}
		})
	}
}

// TestMustGUID_PanicsOnAMalformedID pins that a bad literal cannot reach a
// vtable call.
func TestMustGUID_PanicsOnAMalformedID(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("mustGUID accepted a malformed ID")
		}
	}()

	mustGUID("not-a-guid")
}
