package bridge

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// fakeService exercises every supported method shape through the dispatcher.
type fakeService struct{}

func (fakeService) Echo(s string) (string, error)         { return "echo:" + s, nil }
func (fakeService) Bare() string                          { return "bare" }
func (fakeService) OnlyErr() error                        { return nil }
func (fakeService) OnlyErrFail() error                    { return errors.New("boom") }
func (fakeService) NoArgs()                               {}
func (fakeService) Add(a, b int) (int, error)             { return a + b, nil }
func (fakeService) Panics()                               { panic("kaboom") }
func (fakeService) Save(v map[string]string) (int, error) { return len(v), nil }

func newDispatchServer(t *testing.T) *Server {
	t.Helper()
	s := New(nil)
	s.Register("Fake", &fakeService{})
	return s
}

func raw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func TestInvokeSupportedShapes(t *testing.T) {
	s := newDispatchServer(t)

	cases := []struct {
		name   string
		method string
		args   []json.RawMessage
		want   any
	}{
		{"echo", "Echo", []json.RawMessage{raw("hi")}, "echo:hi"},
		{"bare", "Bare", nil, "bare"},
		{"only-error nil", "OnlyErr", nil, nil},
		{"no args", "NoArgs", nil, nil},
		{"multi args", "Add", []json.RawMessage{raw(2), raw(3)}, 5},
		{"struct arg", "Save", []json.RawMessage{raw(map[string]string{"a": "b"})}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.Invoke("Fake", tc.method, tc.args)
			if err != nil {
				t.Fatalf("invoke %s: %v", tc.method, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("invoke %s = %#v, want %#v", tc.method, got, tc.want)
			}
		})
	}
}

func TestInvokeLoneErrorReturn(t *testing.T) {
	s := newDispatchServer(t)
	got, err := s.Invoke("Fake", "OnlyErrFail", nil)
	if err == nil || err.Error() != "boom" {
		t.Fatalf("err = %v, want boom", err)
	}
	if got != nil {
		t.Fatalf("result = %#v, want nil", got)
	}
}

func TestInvokeUnknownServiceOrMethod(t *testing.T) {
	s := newDispatchServer(t)
	for _, tc := range []struct{ svc, method string }{
		{"Missing", "Echo"},
		{"Fake", "Missing"},
	} {
		_, err := s.Invoke(tc.svc, tc.method, nil)
		want := "unknown method " + tc.svc + "." + tc.method
		if err == nil || err.Error() != want {
			t.Fatalf("invoke %s.%s = %v, want %q", tc.svc, tc.method, err, want)
		}
	}
}

func TestInvokeArgumentErrors(t *testing.T) {
	s := newDispatchServer(t)

	_, err := s.Invoke("Fake", "Echo", nil)
	if err == nil || err.Error() != "invalid argument count for Fake.Echo" {
		t.Fatalf("arg-count error = %v", err)
	}

	// A wrong argument type surfaces the json unmarshal error.
	_, err = s.Invoke("Fake", "Echo", []json.RawMessage{raw(42)})
	if err == nil {
		t.Fatal("wrong argument type accepted")
	}
	var typeErr *json.UnmarshalTypeError
	if !errors.As(err, &typeErr) {
		t.Fatalf("error = %v (%T), want a json.UnmarshalTypeError", err, err)
	}
}

func TestInvokeRecoversPanic(t *testing.T) {
	s := newDispatchServer(t)
	_, err := s.Invoke("Fake", "Panics", nil)
	if err == nil || !strings.Contains(err.Error(), "panic in Fake.Panics: kaboom") {
		t.Fatalf("panic error = %v", err)
	}
	// The server keeps working after a recovered panic.
	if _, err := s.Invoke("Fake", "Bare", nil); err != nil {
		t.Fatalf("invoke after panic: %v", err)
	}
}

// badReturnShape has three returns; badSecondReturn has two where the second
// is not error. Both must fail at registration time, never at request time.
type badReturnShape struct{}

func (badReturnShape) Bad() (int, int, error) { return 0, 0, nil }

type badSecondReturn struct{}

func (badSecondReturn) Bad() (int, int) { return 0, 0 }

func TestRegisterRejectsUnsupportedShapes(t *testing.T) {
	t.Run("three returns", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("registration did not panic")
			}
		}()
		New(nil).Register("Bad", &badReturnShape{})
	})
	t.Run("second return not error", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("registration did not panic")
			}
		}()
		New(nil).Register("Bad", &badSecondReturn{})
	})
}

func TestRegisterRejectsNonPointerOrDuplicate(t *testing.T) {
	t.Run("non-pointer", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("registration did not panic")
			}
		}()
		New(nil).Register("Bad", fakeService{})
	})
	t.Run("duplicate", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("duplicate registration did not panic")
			}
		}()
		s := New(nil)
		s.Register("Fake", &fakeService{})
		s.Register("Fake", &fakeService{})
	})
}
