package auth

import (
	"fmt"

	authv1 "github.com/darmawan01/interchange/auth/gen/go/interchange/auth/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

// Field names the tenant scope is found under when the message does not
// annotate them.
const (
	FieldTenantID  = "tenant_id"
	FieldProjectID = "project_id"
)

// TenantScope is the tenant and project a request names.
type TenantScope struct {
	TenantID  string
	ProjectID string

	// TenantField and ProjectField record which field each value came from,
	// so a denial can say where it looked rather than only what it wanted.
	TenantField  string
	ProjectField string
}

// IsZero reports a request that named no scope at all.
func (s TenantScope) IsZero() bool { return s.TenantID == "" && s.ProjectID == "" }

// TenantScopeOf finds the tenant scope on a request message by reflection:
// whichever fields carry (tenant_id_field) / (project_id_field), and otherwise
// fields named tenant_id / project_id.
//
// Reflection, never a concrete message type: the interceptor runs on every
// service in the process and cannot import any of their generated packages --
// and a binding that imported one would have stopped being an adapter.
func TenantScopeOf(msg proto.Message) (TenantScope, error) {
	var scope TenantScope
	if msg == nil {
		return scope, nil
	}
	m := msg.ProtoReflect()
	if !m.IsValid() && m.Descriptor() == nil {
		return scope, nil
	}

	fields := m.Descriptor().Fields()
	var tenantField, projectField protoreflect.FieldDescriptor
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if fieldFlag(fd, authv1.E_TenantIdField) {
			if tenantField != nil {
				return scope, fmt.Errorf("auth: %s marks both %s and %s as the tenant id",
					m.Descriptor().FullName(), tenantField.Name(), fd.Name())
			}
			tenantField = fd
		}
		if fieldFlag(fd, authv1.E_ProjectIdField) {
			if projectField != nil {
				return scope, fmt.Errorf("auth: %s marks both %s and %s as the project id",
					m.Descriptor().FullName(), projectField.Name(), fd.Name())
			}
			projectField = fd
		}
	}
	// The annotation wins; convention is the fallback, so a message that says
	// nothing still works and a message that says something is believed.
	if tenantField == nil {
		tenantField = fields.ByName(FieldTenantID)
	}
	if projectField == nil {
		projectField = fields.ByName(FieldProjectID)
	}

	var err error
	if scope.TenantID, err = stringField(m, tenantField); err != nil {
		return TenantScope{}, err
	}
	if scope.ProjectID, err = stringField(m, projectField); err != nil {
		return TenantScope{}, err
	}
	if tenantField != nil {
		scope.TenantField = string(tenantField.Name())
	}
	if projectField != nil {
		scope.ProjectField = string(projectField.Name())
	}
	return scope, nil
}

func stringField(m protoreflect.Message, fd protoreflect.FieldDescriptor) (string, error) {
	if fd == nil {
		return "", nil
	}
	if fd.Kind() != protoreflect.StringKind || fd.IsList() || fd.IsMap() {
		return "", fmt.Errorf("auth: %s.%s carries a tenant scope but is %s, not a string",
			m.Descriptor().FullName(), fd.Name(), fd.Kind())
	}
	return m.Get(fd).String(), nil
}

// fieldFlag reads a bool field option, normalising the options message first
// for the same reason AnnotationOf does: a descriptor that did not come from
// generated Go carries the extension as a dynamic value or as unknown bytes,
// and reading it in place would either panic or quietly report false.
func fieldFlag(fd protoreflect.FieldDescriptor, xt protoreflect.ExtensionType) bool {
	opts, ok := fd.Options().(*descriptorpb.FieldOptions)
	if !ok || opts == nil || proto.Size(opts) == 0 {
		return false
	}
	raw, err := proto.Marshal(opts)
	if err != nil {
		return false
	}
	var norm descriptorpb.FieldOptions
	if err := (proto.UnmarshalOptions{Resolver: protoregistry.GlobalTypes}).Unmarshal(raw, &norm); err != nil {
		return false
	}
	if !proto.HasExtension(&norm, xt) {
		return false
	}
	v, _ := proto.GetExtension(&norm, xt).(bool)
	return v
}
