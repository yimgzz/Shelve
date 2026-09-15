package bridge

import (
	"encoding/json"
	"fmt"
	"log"
	"reflect"
)

// errorType is the reflect type of the error interface, used to recognize the
// trailing error return at registration and result-binding time.
var errorType = reflect.TypeOf((*error)(nil)).Elem()

// rpcRequest is one /rpc request frame (master plan §5):
//
//	{"id":1,"svc":"SessionService","method":"Tree","args":[]}
type rpcRequest struct {
	ID     uint64            `json:"id"`
	Svc    string            `json:"svc"`
	Method string            `json:"method"`
	Args   []json.RawMessage `json:"args"`
}

// rpcResponse is one /rpc response frame: exactly one of result/error is set.
type rpcResponse struct {
	ID     uint64          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// serviceEntry is one registered service: its value plus the allow-listed
// exported methods (only these are callable).
type serviceEntry struct {
	value   reflect.Value
	methods map[string]reflect.Method
}

// Register adds a service to the dispatch registry, enumerating its exported
// methods once. A mis-shaped method fails here (registration time) rather than
// at request time. Only registered (service, method) pairs are callable.
func (s *Server) Register(name string, svc any) {
	v := reflect.ValueOf(svc)
	if !v.IsValid() || v.Kind() != reflect.Pointer || v.IsNil() {
		panic(fmt.Sprintf("bridge: register %s: service must be a non-nil pointer", name))
	}
	typ := v.Type()
	methods := make(map[string]reflect.Method, typ.NumMethod())
	for i := 0; i < typ.NumMethod(); i++ {
		m := typ.Method(i)
		validateMethodShape(name, m)
		methods[m.Name] = m
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		panic(fmt.Sprintf("bridge: register %s: server closed", name))
	}
	if _, dup := s.services[name]; dup {
		panic(fmt.Sprintf("bridge: register %s: duplicate service", name))
	}
	s.services[name] = &serviceEntry{value: v, methods: methods}
}

// validateMethodShape enforces the supported result shapes at registration
// time: (), (T), (error) or (T, error).
func validateMethodShape(svc string, m reflect.Method) {
	if m.Type.IsVariadic() {
		panic(fmt.Sprintf("bridge: %s.%s: variadic methods are not supported", svc, m.Name))
	}
	switch m.Type.NumOut() {
	case 0, 1:
		// 0 returns, a plain result, or a lone error.
	case 2:
		if !m.Type.Out(1).Implements(errorType) {
			panic(fmt.Sprintf("bridge: %s.%s: second return value must be error", svc, m.Name))
		}
	default:
		panic(fmt.Sprintf("bridge: %s.%s: unsupported return count %d", svc, m.Name, m.Type.NumOut()))
	}
}

// Invoke dispatches one call to a registered (service, method). Unknown pairs
// and argument-count violations return a usable error without leaking the
// registry; a panic in the service is recovered and reported as an error.
func (s *Server) Invoke(svc, method string, rawArgs []json.RawMessage) (result any, err error) {
	entry, m, ok := s.lookup(svc, method)
	if !ok {
		return nil, fmt.Errorf("unknown method %s.%s", svc, method)
	}
	numIn := m.Type.NumIn() - 1 // exclude the receiver
	if len(rawArgs) != numIn {
		return nil, fmt.Errorf("invalid argument count for %s.%s", svc, method)
	}
	args := make([]reflect.Value, 0, numIn)
	for i := 0; i < numIn; i++ {
		ptr := reflect.New(m.Type.In(i + 1))
		if len(rawArgs[i]) > 0 {
			if uerr := json.Unmarshal(rawArgs[i], ptr.Interface()); uerr != nil {
				return nil, uerr
			}
		}
		args = append(args, ptr.Elem())
	}

	defer func() {
		if r := recover(); r != nil {
			log.Printf("bridge: panic in %s.%s: %v", svc, method, r)
			result, err = nil, fmt.Errorf("panic in %s.%s: %v", svc, method, r)
		}
	}()
	out := m.Func.Call(append([]reflect.Value{entry.value}, args...))
	return bindResults(out)
}

func (s *Server) lookup(svc, method string) (*serviceEntry, reflect.Method, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.services[svc]
	if !ok {
		return nil, reflect.Method{}, false
	}
	m, ok := entry.methods[method]
	if !ok {
		return nil, reflect.Method{}, false
	}
	return entry, m, true
}

// bindResults maps a call's return values to the RPC (result, error) pair.
func bindResults(out []reflect.Value) (any, error) {
	switch len(out) {
	case 0:
		return nil, nil
	case 1:
		if out[0].Type().Implements(errorType) {
			if out[0].IsNil() {
				return nil, nil
			}
			return nil, out[0].Interface().(error)
		}
		return out[0].Interface(), nil
	default:
		// validateMethodShape guarantees the second value is error.
		if out[1].IsNil() {
			return out[0].Interface(), nil
		}
		return nil, out[1].Interface().(error)
	}
}

// encodeResponse builds one response frame. A nil result is encoded
// explicitly as null so the client always sees exactly one of result/error.
func encodeResponse(id uint64, result any, callErr error) ([]byte, error) {
	if callErr != nil {
		return json.Marshal(rpcResponse{ID: id, Error: callErr.Error()})
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return json.Marshal(rpcResponse{ID: id, Result: raw})
}
