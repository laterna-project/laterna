package domain

import "testing"

func TestDownloadFailure(t *testing.T) {
	if (Download{}).Failure() != nil {
		t.Error("failure of a download that did not fail")
	}
	f := Download{State: DownloadFailed, Error: "Conversion failed!"}.Failure()
	if f == nil || f.Key != "download.failed" || f.Params["reason"] != "Conversion failed!" {
		t.Errorf("failure: %+v", f)
	}
}
