package authz

const ResourceSMS = "sms"

var (
	SMSRead    = Permission{Resource: ResourceSMS, Action: ActionRead}
	SMSOperate = Permission{Resource: ResourceSMS, Action: ActionOperate}
)

func init() {
	RegisterResource(ResourceDefinition{Resource: ResourceSMS, LabelKey: "SMS notifications", Actions: []ActionDefinition{
		{Action: ActionRead, LabelKey: "View SMS notifications", DescriptionKey: "View all accounts' SMS history and incoming replies without sharing credentials.", DefaultRoles: []string{BuiltInRoleAdmin}},
		{Action: ActionOperate, LabelKey: "Send SMS notifications", DescriptionKey: "Send notifications to original recipients and retrieve incoming replies.", DefaultRoles: []string{BuiltInRoleAdmin}},
	}})
}
