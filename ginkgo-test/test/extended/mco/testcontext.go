package mco

// SharedContext is designed to store test context info as k,v pairs
// it can be used to share context values thru all test funcs
// test struct can be inherited from this, it can store all kinds of data, primitive or struct
// you can get the str,int,bool values. i.e. they're frequently used data types.
// note: this impl is not thread safe, dont' use it for concurrent scenario.
type SharedContext struct {
	ctx map[string]interface{}
}

// NewSharedContext constructor
func NewSharedContext() *SharedContext {
	return &SharedContext{
		ctx: make(map[string]interface{}),
	}
}

// Put store k,v data in map
func (sc *SharedContext) Put(k string, v interface{}) {
	sc.ctx[k] = v
}

// Get get object by key, it will return original data
func (sc *SharedContext) Get(k string) interface{} {
	return sc.ctx[k]
}

// StrValue get value by key and convert it to string
// if data type assertion failed, return empty string
func (sc *SharedContext) StrValue(k string) string {
	if v, ok := sc.Get(k).(string); ok {
		return v
	}
	return ""
}

// IntValue get value by key and convert it to integer
// if data type assertion failed, return zero
func (sc *SharedContext) IntValue(k string) int {
	if v, ok := sc.Get(k).(int); ok {
		return v
	}
	return 0
}

// BoolValue get value by key and conver it to boolean
// if data type assertion failed, return false
func (sc *SharedContext) BoolValue(k string) bool {
	if v, ok := sc.Get(k).(bool); ok {
		return v
	}
	return false
}
