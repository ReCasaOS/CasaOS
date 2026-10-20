package common

import (
	"github.com/ReCasaOS/CasaOS/codegen/message_bus"
)

// resourceProperties are the properties of the two resource alert events.
func resourceProperties() []message_bus.PropertyType {
	var list []message_bus.PropertyType
	for _, name := range []string{"item", "name", "kind", "situation", "value", "since", "source", "minutes"} {
		list = append(list, message_bus.PropertyType{Name: name})
	}
	return list
}

// devtype -> action -> event
var EventTypes = []message_bus.EventType{
	{Name: "casaos:system:utilization", SourceID: SERVICENAME, PropertyTypeList: []message_bus.PropertyType{}},
	{Name: "casaos:file:recover", SourceID: SERVICENAME, PropertyTypeList: []message_bus.PropertyType{}},
	{Name: "casaos:file:operate", SourceID: SERVICENAME, PropertyTypeList: []message_bus.PropertyType{}},
	{Name: "resource:alert-firing", SourceID: SERVICENAME, PropertyTypeList: resourceProperties()},
	{Name: "resource:alert-resolved", SourceID: SERVICENAME, PropertyTypeList: resourceProperties()},
}
