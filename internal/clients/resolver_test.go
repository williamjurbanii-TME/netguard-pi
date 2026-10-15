package clients

import "testing"

func TestParseNeighbors(t *testing.T) {
	text := `
Interface: 192.168.1.10 --- 0xb
  Internet Address      Physical Address      Type
  192.168.1.1           a4-83-e7-00-00-01     dynamic
  192.168.1.255         ff-ff-ff-ff-ff-ff     static
Interface 12: Wi-Fi
Internet Address                              Physical Address   Type
--------------------------------------------  -----------------  -----------
fe80::1c2b:3d4e:5f60:7182                     06-11-22-33-44-55  Stale
2600:1700:abcd::25                            06-11-22-33-44-55  Reachable
ff02::1                                       33-33-00-00-00-01  Permanent
192.168.1.20 dev eth0 lladdr da:a1:19:00:00:20 REACHABLE
fe80::99 dev eth0 lladdr da:a1:19:00:00:20 STALE
192.168.1.77 dev eth0 FAILED
? (192.168.1.30) at 3c:22:fb:00:00:30 on en0 ifscope [ethernet]
`
	got := map[string]string{}
	parseNeighbors(text, got)
	want := map[string]string{
		"192.168.1.1":               "a4:83:e7:00:00:01",
		"fe80::1c2b:3d4e:5f60:7182": "06:11:22:33:44:55",
		"2600:1700:abcd::25":        "06:11:22:33:44:55",
		"192.168.1.20":              "da:a1:19:00:00:20",
		"fe80::99":                  "da:a1:19:00:00:20",
		"192.168.1.30":              "3c:22:fb:00:00:30",
	}
	if len(got) != len(want) {
		t.Errorf("got %d entries, want %d: %v", len(got), len(want), got)
	}
	for ip, mac := range want {
		if got[ip] != mac {
			t.Errorf("%s -> %q, want %q", ip, got[ip], mac)
		}
	}
}
