package extended

import (
	"encoding/json"
	"fmt"
	"strings"

	logger "github.com/openshift/machine-config-operator/test/extended/util/logext"

	"k8s.io/client-go/util/jsonpath"
	e2e "k8s.io/kubernetes/test/e2e/framework"
)

// JSON function creates a JSONData struct from a string with json format
func JSON(jsonString string) *JSONData {
	jsonData := JSONData{nil}

	if strings.Trim(jsonString, " ") == "" {
		return &jsonData
	}

	if err := json.Unmarshal([]byte(jsonString), &jsonData.data); err != nil {
		logger.Errorf("Data is not in json format:\n %s", jsonString)
		return nil
	}
	return &jsonData
}

// JSONData provides the functionliaty to manipulate data in json format
type JSONData struct {
	data interface{}
}

// AsJSONString returns a JSON string representation fo the stored value
func (jd *JSONData) AsJSONString() (string, error) {
	text, err := json.MarshalIndent(jd.data, "", "    ")

	return string(text), err
}

// ToFloat returns the stored value as float64
func (jd *JSONData) ToFloat() float64 {
	return jd.data.(float64)
}

// ToInt returns the stored value as Int. If float, it will be transformed to Int
func (jd *JSONData) ToInt() int {
	return int(jd.data.(float64))
}

// ToBool returns the stored value as bool.
func (jd *JSONData) ToBool() bool {
	return jd.data.(bool)
}

// ToString returns the stored value as string
func (jd *JSONData) ToString() string {
	return jd.data.(string)

}

// ToMap returns the stored value as map[string]interface{}
func (jd *JSONData) ToMap() map[string]interface{} {
	return jd.data.(map[string]interface{})
}

// ToList returns the stored value as []interface{}
func (jd *JSONData) ToList() []interface{} {
	return jd.data.([]interface{})

}

// ToInterface returns the raw stored value
func (jd *JSONData) ToInterface() interface{} {
	return jd.data

}

// Exists is true if the stored value is not nil
func (jd *JSONData) Exists() bool {
	return jd.data != nil
}

// String implements the stringer interface
func (jd *JSONData) String() string {
	result, err := jd.AsJSONString()
	if err != nil {
		return fmt.Sprintf("%v", jd.data)
	}
	return result
}

// DeleteSafe deletes a key in a map json node
func (jd *JSONData) DeleteSafe(key string) error {
	if jd.data == nil {
		return fmt.Errorf("Data does not exist. It is empty: %v", jd.data)
	}

	mapData, ok := jd.data.(map[string]interface{})
	if !ok {
		return fmt.Errorf("Data is not a map: %v", jd.data)
	}

	delete(mapData, key)

	return nil
}

// Delete deletes the a key in a map json node
func (jd *JSONData) Delete(key string) {
	err := jd.DeleteSafe(key)
	if err != nil {
		e2e.Failf("Could not DELETE key [%s] from json data [%s]. Error: %v", key, jd.data, err)
	}
}

// PutSafe set the value of a key in a map json node
func (jd *JSONData) PutSafe(key string, value interface{}) error {
	if jd.data == nil {
		return fmt.Errorf("Data does not exist. It is empty: %v", jd.data)
	}

	mapData, ok := jd.data.(map[string]interface{})
	if !ok {
		return fmt.Errorf("Data is not a map: %v", jd.data)
	}

	mapData[key] = value

	return nil
}

// Put sets the value of a key in a map json node
func (jd *JSONData) Put(key, value string) {
	err := jd.PutSafe(key, value)
	if err != nil {
		e2e.Failf("Could not PUT key [%s] value [%s] in json data [%s]. Error: %v", key, value, jd.data, err)
	}

}

// GetSafe returns the value of a key in the data and an error
func (jd JSONData) GetSafe(key string) (*JSONData, error) {
	if jd.data == nil {
		return nil, fmt.Errorf("Data does not exist. It is empty: %v", jd.data)
	}

	mapData, ok := jd.data.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("Data is not a map: %v", jd.data)
	}
	value, found := mapData[key]
	if found {
		return &JSONData{value}, nil
	}
	return &JSONData{nil}, nil
}

// ItemSafe returns the value of a given item in a list and an error
func (jd JSONData) ItemSafe(index int) (*JSONData, error) {
	if jd.data == nil {
		return nil, fmt.Errorf("Data does not exist. It is empty: %v", jd.data)
	}

	listData, ok := jd.data.([]interface{})
	if !ok {
		return nil, fmt.Errorf("Data is not a list: %v", jd.data)
	}
	return &JSONData{listData[index]}, nil
}

// Get returns the value of a key in the data, in case of error the returned value is nil
func (jd JSONData) Get(key string) *JSONData {
	value, err := jd.GetSafe(key)
	if err != nil {
		e2e.Failf("Could not get key [%s]. Error: %v", key, err)
	}

	return value
}

// Item returns the value of a given item in a list, in case of error the returned value is nil
func (jd JSONData) Item(index int) *JSONData {
	value, err := jd.ItemSafe(index)
	if err != nil {
		e2e.Failf("Could not get item [%d]. Error: %v", index, err)
	}

	return value
}

// Items returns all values in a list as JSONData structs.
func (jd JSONData) Items() []*JSONData {
	if jd.data == nil {
		e2e.Failf("Data does not exist. It is empty: %v", jd.data)
	}

	listData, ok := jd.data.([]interface{})
	if !ok {
		e2e.Failf("Data is not a list: %v", jd.data)
	}

	ret := []*JSONData{}
	for _, data := range listData {
		ret = append(ret, &JSONData{data})
	}

	return ret
}

// GetRawValue will return the jsonPath output as it is, without any kind of flattening or property transformation
func (jd *JSONData) GetRawValue(jsonPath string) ([]interface{}, error) {
	j := jsonpath.New("parser: " + jsonPath)

	if err := j.Parse(jsonPath); err != nil {
		return nil, err
	}

	fullResults, err := j.FindResults(jd.data)
	if err != nil {
		return nil, err
	}

	returnResults := make([]interface{}, 0, len(fullResults))
	for _, result := range fullResults {

		res := make([]interface{}, 0, len(result))
		for i := range result {
			res = append(res, result[i].Interface())
		}
		returnResults = append(returnResults, res)
	}

	return returnResults, nil
}

// GetJSONPath will return a flattened list of JSONData structs with the values matching the jsonpath expression
func (jd *JSONData) GetJSONPath(jsonPath string) ([]JSONData, error) {
	allResults, err := jd.GetRawValue(jsonPath)
	if err != nil {
		return nil, err
	}

	flatResults := flattenResults(allResults)
	return flatResults, err
}

func flattenResults(allExpresults []interface{}) []JSONData {
	flatResults := []JSONData{}
	for i := range allExpresults {
		var expression = allExpresults[i].([]interface{})
		for _, result := range expression {
			flatResults = append(flatResults, JSONData{result})
		}
	}

	return flatResults
}
