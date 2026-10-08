package weather

import "testing"

func TestNormalizeQuery(t *testing.T) {
	cases := map[string]string{
		"":                    "",
		"  Đà   Lạt ":         "da lat",
		"Ðà Lạt":              "da lat", // GeoNames' look-alike Ð (U+00D0)
		"Hồ Chí Minh":         "ho chi minh",
		"Thừa Thiên Huế":      "thua thien hue",
		"Sài Gòn":             "sai gon",
		"TP.HCM":              "tp.hcm",
		"New York":            "new york",
		"Buôn Ma Thuột":       "buon ma thuot",
		"Phan Rang–Tháp Chàm": "phan rang–thap cham",
	}
	for in, want := range cases {
		if got := normalizeQuery(in); got != want {
			t.Errorf("normalizeQuery(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseCoords(t *testing.T) {
	valid := map[string][2]float64{
		"10.74111,106.71806": {10.74111, 106.71806},
		" 10.74, 106.72 ":    {10.74, 106.72},
		"10.74 106.72":       {10.74, 106.72},
		"10.74;106.72":       {10.74, 106.72},
		"-33.8688, 151.2093": {-33.8688, 151.2093},
		"+40.7128 -74.0060":  {40.7128, -74.006},
		"0,0":                {0, 0},
	}
	for in, want := range valid {
		p, ok := parseCoords(in)
		if !ok || p.Latitude != want[0] || p.Longitude != want[1] {
			t.Errorf("parseCoords(%q) = %+v, %v, want %v", in, p, ok, want)
		}
	}
	if p, _ := parseCoords(" 10.74 , 106.72 "); p.Name != "10.74, 106.72" {
		t.Errorf("parseCoords name = %q", p.Name)
	}
	for _, in := range []string{"", "hcm", "10.74", "91,0", "0,181", "10.74,106.72,5", "10,74 106,72", "Quận 7"} {
		if p, ok := parseCoords(in); ok {
			t.Errorf("parseCoords(%q) = %+v, want no match", in, p)
		}
	}
}

func TestPickPlace(t *testing.T) {
	jp := place{Name: "Tokyo", CountryCode: "JP"}
	vn := place{Name: "Huế", CountryCode: "VN"}
	et := place{Name: "Humera", CountryCode: "ET"}

	if got, ok := pickPlace([]place{et, vn}); !ok || got != vn {
		t.Errorf("pickPlace prefers VN: got %+v, %v", got, ok)
	}
	if got, ok := pickPlace([]place{jp, et}); !ok || got != jp {
		t.Errorf("pickPlace falls back to first: got %+v, %v", got, ok)
	}
	if _, ok := pickPlace(nil); ok {
		t.Error("pickPlace(nil) ok = true, want false")
	}
}

func TestDisplayName(t *testing.T) {
	cases := []struct {
		p    place
		want string
	}{
		{hcmPlace, "Thành phố Hồ Chí Minh"},
		{tanThuanPlace, "Tân Thuận, Thành phố Hồ Chí Minh"},
		{place{Name: "Hà Nội", Admin1: "Hanoi", CountryCode: "VN"}, "Hà Nội"},
		{place{Name: "Vũng Tàu", Admin1: "Thành phố Hồ Chí Minh", CountryCode: "VN"}, "Vũng Tàu, Thành phố Hồ Chí Minh"},
		{place{Name: "Tokyo", Admin1: "Tokyo", CountryCode: "JP", Country: "Nhật Bản"}, "Tokyo, Nhật Bản"},
	}
	for _, c := range cases {
		if got := displayName(c.p); got != c.want {
			t.Errorf("displayName(%+v) = %q, want %q", c.p, got, c.want)
		}
	}
}
