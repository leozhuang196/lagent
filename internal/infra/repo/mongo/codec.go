package mongo

import (
	"encoding/json"
	"fmt"
	"reflect"

	"go.mongodb.org/mongo-driver/v2/bson"
)

var (
	tAnyMap = reflect.TypeOf(map[string]any(nil))
	tRaw    = reflect.TypeOf(bson.Raw(nil))
)

// Registry is the BSON registry every collection uses. It differs from the
// driver default in one place: free-form map[string]any fields (audit details)
// round-trip with JSON semantics — nested objects come back as map[string]any,
// arrays as []any, numbers as float64 — exactly what the memory backend and
// the HTTP layer produce. The driver default would decode nested values as
// bson.D / bson.A / int32, which callers type-asserting on JSON shapes would
// trip over.
//
// json.RawMessage fields keep the driver default: BSON binary (subtype 0).
// That round-trips byte-for-byte; those fields are opaque payloads that are
// never filtered on.
var Registry = newRegistry()

func newRegistry() *bson.Registry {
	r := bson.NewRegistry()
	r.RegisterTypeEncoder(tAnyMap, bson.ValueEncoderFunc(encodeAnyMap))
	r.RegisterTypeDecoder(tAnyMap, bson.ValueDecoderFunc(decodeAnyMap))
	return r
}

// encodeAnyMap normalizes the map through JSON (times become RFC 3339
// strings, structs become objects) and writes it as an embedded document.
func encodeAnyMap(ec bson.EncodeContext, vw bson.ValueWriter, val reflect.Value) error {
	if !val.IsValid() || val.Type() != tAnyMap {
		return fmt.Errorf("encodeAnyMap: unexpected %v", val)
	}
	if val.IsNil() {
		return vw.WriteNull()
	}
	js, err := json.Marshal(val.Interface())
	if err != nil {
		return err
	}
	var raw bson.Raw
	if err := bson.UnmarshalExtJSON(js, false, &raw); err != nil {
		return err
	}
	enc, err := ec.LookupEncoder(tRaw)
	if err != nil {
		return err
	}
	return enc.EncodeValue(ec, vw, reflect.ValueOf(raw))
}

// decodeAnyMap reads an embedded document and converts it to JSON shapes.
func decodeAnyMap(dc bson.DecodeContext, vr bson.ValueReader, val reflect.Value) error {
	if !val.CanSet() || val.Type() != tAnyMap {
		return fmt.Errorf("decodeAnyMap: unexpected %v", val)
	}
	if vr.Type() == bson.TypeNull {
		val.Set(reflect.Zero(tAnyMap))
		return vr.ReadNull()
	}
	dec, err := dc.LookupDecoder(tRaw)
	if err != nil {
		return err
	}
	raw := reflect.New(tRaw).Elem()
	if err := dec.DecodeValue(dc, vr, raw); err != nil {
		return err
	}
	js, err := bson.MarshalExtJSON(raw.Interface(), false, false)
	if err != nil {
		return err
	}
	var m map[string]any
	if err := json.Unmarshal(js, &m); err != nil {
		return err
	}
	val.Set(reflect.ValueOf(m))
	return nil
}
