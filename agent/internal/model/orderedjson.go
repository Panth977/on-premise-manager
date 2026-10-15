package model

import (
	"bytes"
	"encoding/json"
	"errors"
)

// OrderedField is one key of an OrderedObject.
type OrderedField struct {
	Key   string
	Value json.RawMessage
}

// OrderedObject is a JSON object that keeps its key order (agent.json view + patch, §21 #90: unknown keys and the key
// order are preserved when the GUI changes a setting).
type OrderedObject []OrderedField

// MarshalJSON writes the keys in order.
func (o OrderedObject) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, f := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(f.Key)
		b.Write(k)
		b.WriteByte(':')
		if len(f.Value) == 0 {
			b.WriteString("null")
		} else {
			b.Write(f.Value)
		}
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// UnmarshalJSON reads an object keeping the key order (a repeated key keeps its first position, last value).
func (o *OrderedObject) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	t, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := t.(json.Delim); !ok || d != '{' {
		return errors.New("not a JSON object")
	}
	out := OrderedObject{}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return err
		}
		k, _ := kt.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return err
		}
		out = out.Set(k, v)
	}
	*o = out
	return nil
}

// Get returns the raw value of a key.
func (o OrderedObject) Get(key string) (json.RawMessage, bool) {
	for _, f := range o {
		if f.Key == key {
			return f.Value, true
		}
	}
	return nil, false
}

// Set replaces the value of a key in place, or appends the key.
func (o OrderedObject) Set(key string, v json.RawMessage) OrderedObject {
	for i := range o {
		if o[i].Key == key {
			o[i].Value = v
			return o
		}
	}
	return append(o, OrderedField{Key: key, Value: v})
}

// SetValue marshals v and sets it.
func (o OrderedObject) SetValue(key string, v any) OrderedObject {
	b, _ := json.Marshal(v)
	return o.Set(key, b)
}
