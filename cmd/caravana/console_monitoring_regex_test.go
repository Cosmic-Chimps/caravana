package main

import (
	"strings"
	"testing"
)

// The lab step's regular expressions read the console's markup; this checks them against markup of
// the shapes the page draws, since the step itself only runs in the lab.
func TestMonitoringLabRegexes(t *testing.T) {
	host := `<div class="cv-mnmeter cv-mnmeter-split">
  <span class="cv-mnmeter-label">mem</span>
  <svg class="cv-mnmeter-svg" viewBox="0 0 100 6"><title>x</title><rect class="cv-mntrack"/></svg>
  <span class="cv-mnmeter-read cv-mnwarntext"><span class="cv-mark cv-mark-warning" aria-hidden="true"></span> 1.7G used · 1.3G reserved of 1.9G<span class="sr-only"> (mostly in use)</span></span>
</div>
<div class="cv-mnmeter cv-mnmeter-split">
  <span class="cv-mnmeter-label">cpu</span>
  <svg class="cv-mnmeter-svg" viewBox="0 0 100 6"><title>x</title></svg>
  <span class="cv-mnmeter-read">1.2 used · 0.8 reserved of 2 vCPU</span>
</div>
<div class="cv-mnmeter">
  <span class="cv-mnmeter-label">disk</span>
  <svg class="cv-mnmeter-svg" viewBox="0 0 100 6"><title>x</title></svg>
  <span class="cv-mnmeter-read cv-mnwarntext"><span class="cv-mark cv-mark-failed" aria-hidden="true"></span> 39/40G<span class="sr-only"> (full)</span></span>
</div>`
	got := map[string]string{}
	for _, m := range cmMeterRe.FindAllStringSubmatch(host, -1) {
		got[m[1]] = pageText(m[2])
	}
	want := map[string]string{"mem": "1.7G used · 1.3G reserved of 1.9G", "cpu": "1.2 used · 0.8 reserved of 2 vCPU", "disk": "39/40G"}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("meter %s reads %q, want %q", k, got[k], w)
		}
	}

	row := `<div class="cv-mnrow" role="row" id="mon-row-slow-web">
      <span class="cv-mark cv-mark-lg cv-mark-warning" role="cell" aria-label="unusual" title="unusual"></span>
      <div role="cell" class="cv-mnname"><a href="/apps/slow" id="mon-row-slow-web-link" class="cv-mnname-text">slow</a><span class="cv-mncopies">1 copy</span></div>
      <div role="cell" class="cv-mnnumber" data-label="Requests">96.0/s</div>
      <div role="cell" class="cv-mncpu" data-label="CPU"><svg class="cv-mnspark"><title>t</title></svg><span class="cv-mncpu-read" title="0.4 of 0.5 vCPU reserved, now">0.4/0.5 vCPU</span></div>
      <div role="cell" class="cv-mnlogscell"><a href="/logs?app=slow&amp;service=slow%2Fweb" id="mon-row-slow-web-logs" class="cv-mnlogs" aria-label="Logs of slow">x</a></div>
</div>`
	rows := cmRowsOf(row)
	if len(rows) != 1 {
		t.Fatalf("%d rows", len(rows))
	}
	r := rows[0]
	if r.Name != "slow" || r.Href != "/apps/slow" || r.Copies != "1 copy" || r.Mark != "warning" || r.MarkWord != "unusual" ||
		r.LogsHref != "/logs?app=slow&service=slow%2Fweb" || r.CPU != "0.4/0.5 vCPU" || r.Cells["Requests"] != "96.0/s" {
		t.Errorf("row = %+v", r)
	}
	if !strings.Contains(r.LogsHref, "service=slow%2Fweb") {
		t.Error("the logs link has no service")
	}
}
