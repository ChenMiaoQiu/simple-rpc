package simplerpc

import (
	"go/ast"
	"log"
	"reflect"
	"sync/atomic"
)

type service struct {
	name   string                 // struct name
	typ    reflect.Type           // struct type
	rcvr   reflect.Value          // struct instance
	method map[string]*methodType // struct method
}

// newService build a new service by struct
func newService(rcvr interface{}) *service {
	s := new(service)
	s.rcvr = reflect.ValueOf(rcvr)
	s.name = reflect.Indirect(s.rcvr).Type().Name()
	s.typ = reflect.TypeOf(rcvr)
	if !ast.IsExported(s.name) {
		log.Fatalf("rpc server: %s is not a valid service name", s.name)
	}
	s.registerMethods()
	return s
}

// registerMethods register struct method to service
func (s *service) registerMethods() {
	s.method = make(map[string]*methodType)

	// common method func (t *T) MethodName(argType T1, replyType *T2) error
	for i := 0; i < s.typ.NumMethod(); i++ {
		method := s.typ.Method(i)
		// check method format
		mType := method.Type
		if mType.NumIn() != 3 || mType.NumOut() != 1 {
			continue
		}
		if mType.Out(0) != reflect.TypeOf((*error)(nil)).Elem() {
			continue
		}
		argType, replyType := mType.In(1), mType.In(2)
		if !isExportedOrBuiltinType(argType) || !isExportedOrBuiltinType(replyType) {
			continue
		}

		// store method to service
		s.method[method.Name] = &methodType{
			method:    method,
			ArgType:   argType,
			ReplyType: replyType,
		}
		log.Printf("rpc server: register %s.%s\n", s.name, method.Name)
	}
}

// isExportedOrBuiltinType check method func
func isExportedOrBuiltinType(t reflect.Type) bool {
	return ast.IsExported(t.Name()) || t.PkgPath() == ""
}

// call use reflect to use method
func (s *service) call(m *methodType, argv, replyv reflect.Value) error {
	// add use method cnt
	atomic.AddUint64(&m.numCalls, 1)
	f := m.method.Func
	returnValues := f.Call([]reflect.Value{s.rcvr, argv, replyv})
	if errInter := returnValues[0].Interface(); errInter != nil {
		return errInter.(error)
	}
	return nil
}
