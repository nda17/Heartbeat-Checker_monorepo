package service

import (
	"errors"
	"testing"
	"time"
)

func TestPasswordWorkRejectsSaturationAndRecovers(t *testing.T) {
	svc := New(nil, nil, nil, nil, Config{})
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, _, err := svc.passwordWork(func() (string, bool, error) {
				entered <- struct{}{}
				<-release
				return "done", true, nil
			})
			results <- err
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			close(release)
			t.Fatal("password work did not start both admitted operations")
		}
	}
	called := false
	_, _, err := svc.passwordWork(func() (string, bool, error) {
		called = true
		return "unexpected", false, nil
	})
	if !errors.Is(err, ErrBusy) || called {
		t.Fatalf("saturated password work error=%v called=%t, want ErrBusy without running work", err, called)
	}
	close(release)
	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("admitted password work error=%v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("admitted password work did not finish after release")
		}
	}
	_, _, err = svc.passwordWork(func() (string, bool, error) { return "recovered", true, nil })
	if err != nil {
		t.Fatalf("password work remained blocked after recovery: %v", err)
	}
}
