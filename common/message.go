package common

import (
	"github.com/ReCasaOS/CasaOS/codegen/message_bus"
)

// resourceProperties are the properties of the two resource alert events. A literal, not built by a call:
// the package must have nothing to run when it is initialised (see package boot).
var resourceProperties = []message_bus.PropertyType{
	{Name: "item"}, {Name: "name"}, {Name: "kind"}, {Name: "situation"}, {Name: "value"}, {Name: "since"}, {Name: "source"}, {Name: "minutes"},
}

// devtype -> action -> event
var EventTypes = []message_bus.EventType{
	{Name: "casaos:system:utilization", SourceID: SERVICENAME, PropertyTypeList: []message_bus.PropertyType{}},
	{Name: "casaos:file:recover", SourceID: SERVICENAME, PropertyTypeList: []message_bus.PropertyType{}},
	{Name: "casaos:file:operate", SourceID: SERVICENAME, PropertyTypeList: []message_bus.PropertyType{}},
	{Name: "resource:alert-firing", SourceID: SERVICENAME, PropertyTypeList: resourceProperties},
	{Name: "resource:alert-resolved", SourceID: SERVICENAME, PropertyTypeList: resourceProperties},
}
