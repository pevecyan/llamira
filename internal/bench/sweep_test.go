package bench

import "testing"

func TestPlanAndRecommend(t *testing.T) {
	p := SweepRequest{MinW: 100, MaxW: 170, StepW: 25}.Plan()
	if len(p) != 4 || p[0] != 100 || p[3] != 170 { // 100,125,150,170; 50/53 = 94% < 95%, so balanced is 150 W
		t.Errorf("plan %v", p)
	}
	steps := []SweepStep{
		{Watts: 100, GenTPS: 40, AvgPowerW: 180, MaxTempC: 55},
		{Watts: 125, GenTPS: 50, AvgPowerW: 210, MaxTempC: 60},
		{Watts: 150, GenTPS: 52.5, AvgPowerW: 230, MaxTempC: 66},
		{Watts: 170, GenTPS: 53, AvgPowerW: 240, MaxTempC: 72},
	}
	r := recommend(steps, nil)
	if r["max"].Watts != 170 || r["balanced"].Watts != 150 || r["efficient"].Watts != 125 {
		t.Errorf("picks max=%v balanced=%v efficient=%v", r["max"], r["balanced"], r["efficient"])
	}
	if r["balanced"].Reason == "" {
		t.Error("reason missing")
	}
}
