package rally

import (
	"bytes"
	"encoding/json"
)

// Info is the info object of the OpenAPI document. Fields follow the OpenAPI 3.1 object of the same name.
type Info struct {
	Title          string   `json:"title"`
	Summary        string   `json:"summary,omitempty"`
	Description    string   `json:"description,omitempty"`
	TermsOfService string   `json:"termsOfService,omitempty"`
	Contact        *Contact `json:"contact,omitempty"`
	License        *License `json:"license,omitempty"`
	Version        string   `json:"version"`
}

// Contact is the contact information of the API. Fields follow the OpenAPI 3.1 object of the same name.
type Contact struct {
	Name  string `json:"name,omitempty"`
	URL   string `json:"url,omitempty"`
	Email string `json:"email,omitempty"`
}

// License is the license of the API. Fields follow the OpenAPI 3.1 object of the same name.
type License struct {
	Name       string `json:"name"`
	Identifier string `json:"identifier,omitempty"`
	URL        string `json:"url,omitempty"`
}

// Server is a server of the API, as listed in the document. Fields follow the OpenAPI 3.1 object of the same name.
type Server struct {
	URL         string                     `json:"url"`
	Description string                     `json:"description,omitempty"`
	Variables   map[string]*ServerVariable `json:"variables,omitempty"`
}

// ServerVariable is a variable of a Server URL. Fields follow the OpenAPI 3.1 object of the same name.
type ServerVariable struct {
	Enum        []string `json:"enum,omitempty"`
	Default     string   `json:"default"`
	Description string   `json:"description,omitempty"`
}

// Tag describes a tag used by operations. Fields follow the OpenAPI 3.1 object of the same name.
type Tag struct {
	Name         string        `json:"name"`
	Description  string        `json:"description,omitempty"`
	ExternalDocs *ExternalDocs `json:"externalDocs,omitempty"`
}

// ExternalDocs links to documentation outside the document. Fields follow the OpenAPI 3.1 object of the same name.
type ExternalDocs struct {
	Description string `json:"description,omitempty"`
	URL         string `json:"url"`
}

// SecurityScheme is a security scheme of the API (http, apiKey, oauth2, openIdConnect). Fields follow the OpenAPI 3.1 object of the same name.
type SecurityScheme struct {
	Type             string      `json:"type"`
	Description      string      `json:"description,omitempty"`
	Name             string      `json:"name,omitempty"`
	In               string      `json:"in,omitempty"`
	Scheme           string      `json:"scheme,omitempty"`
	BearerFormat     string      `json:"bearerFormat,omitempty"`
	Flows            *OAuthFlows `json:"flows,omitempty"`
	OpenIDConnectURL string      `json:"openIdConnectUrl,omitempty"`
}

// OAuthFlows lists the OAuth flows of a SecurityScheme. Fields follow the OpenAPI 3.1 object of the same name.
type OAuthFlows struct {
	Implicit          *OAuthFlow `json:"implicit,omitempty"`
	Password          *OAuthFlow `json:"password,omitempty"`
	ClientCredentials *OAuthFlow `json:"clientCredentials,omitempty"`
	AuthorizationCode *OAuthFlow `json:"authorizationCode,omitempty"`
}

// OAuthFlow is an OAuth flow and its scopes. Fields follow the OpenAPI 3.1 object of the same name.
type OAuthFlow struct {
	AuthorizationURL string            `json:"authorizationUrl,omitempty"`
	TokenURL         string            `json:"tokenUrl,omitempty"`
	RefreshURL       string            `json:"refreshUrl,omitempty"`
	Scopes           map[string]string `json:"scopes"`
}

type document struct {
	OpenAPI      string                       `json:"openapi"`
	Info         Info                         `json:"info"`
	Servers      []*Server                    `json:"servers,omitempty"`
	Paths        map[string]map[string]*opDoc `json:"paths"`
	Components   components                   `json:"components"`
	Security     []map[string][]string        `json:"security,omitempty"`
	Tags         []*Tag                       `json:"tags,omitempty"`
	ExternalDocs *ExternalDocs                `json:"externalDocs,omitempty"`
}

type components struct {
	Schemas         map[string]*schema         `json:"schemas,omitempty"`
	SecuritySchemes map[string]*SecurityScheme `json:"securitySchemes,omitempty"`
}

type opDoc struct {
	OperationID string                `json:"operationId"`
	Summary     string                `json:"summary,omitempty"`
	Description string                `json:"description,omitempty"`
	Tags        []string              `json:"tags,omitempty"`
	Parameters  []*param              `json:"parameters,omitempty"`
	RequestBody *requestBody          `json:"requestBody,omitempty"`
	Responses   map[string]*response  `json:"responses"`
	Deprecated  bool                  `json:"deprecated,omitempty"`
	Security    []map[string][]string `json:"security,omitzero"`
}

type param struct {
	Name        string  `json:"name"`
	In          string  `json:"in"`
	Description string  `json:"description,omitempty"`
	Required    bool    `json:"required,omitempty"`
	Schema      *schema `json:"schema"`
}

type requestBody struct {
	Content  map[string]*mediaType `json:"content"`
	Required bool                  `json:"required,omitempty"`
}

type mediaType struct {
	Schema *schema `json:"schema"`
}

type response struct {
	Description string                `json:"description"`
	Headers     map[string]*header    `json:"headers,omitempty"`
	Content     map[string]*mediaType `json:"content,omitempty"`
}

type header struct {
	Description string  `json:"description,omitempty"`
	Schema      *schema `json:"schema"`
}

type schema struct {
	Ref                  string             `json:"$ref,omitempty"`
	Type                 string             `json:"type,omitempty"`
	Nullable             bool               `json:"-"`
	Format               string             `json:"format,omitempty"`
	Description          string             `json:"description,omitempty"`
	Default              any                `json:"default,omitempty"`
	Examples             []any              `json:"examples,omitempty"`
	Enum                 []any              `json:"enum,omitempty"`
	Items                *schema            `json:"items,omitempty"`
	Properties           map[string]*schema `json:"properties,omitempty"`
	AdditionalProperties any                `json:"additionalProperties,omitempty"`
	Required             []string           `json:"required,omitempty"`
	Minimum              json.Number        `json:"minimum,omitempty"`
	Maximum              json.Number        `json:"maximum,omitempty"`
	ExclusiveMinimum     json.Number        `json:"exclusiveMinimum,omitempty"`
	ExclusiveMaximum     json.Number        `json:"exclusiveMaximum,omitempty"`
	MultipleOf           json.Number        `json:"multipleOf,omitempty"`
	MinLength            *int               `json:"minLength,omitempty"`
	MaxLength            *int               `json:"maxLength,omitempty"`
	Pattern              string             `json:"pattern,omitempty"`
	MinItems             *int               `json:"minItems,omitempty"`
	MaxItems             *int               `json:"maxItems,omitempty"`
	UniqueItems          bool               `json:"uniqueItems,omitempty"`
	MinProperties        *int               `json:"minProperties,omitempty"`
	MaxProperties        *int               `json:"maxProperties,omitempty"`
	AnyOf                []*schema          `json:"anyOf,omitempty"`

	propertyOrder []string
}

func (s *schema) MarshalJSON() ([]byte, error) {
	type plain schema
	tmp := *(*plain)(s)
	tmp.Type, tmp.Properties = "", nil
	rest, err := json.Marshal(&tmp)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteByte('{')
	if s.Type != "" {
		if s.Nullable {
			b.WriteString(`"type":["` + s.Type + `","null"]`)
		} else {
			b.WriteString(`"type":"` + s.Type + `"`)
		}
	}
	if len(rest) > 2 {
		if b.Len() > 1 {
			b.WriteByte(',')
		}
		b.Write(rest[1 : len(rest)-1])
	}
	if len(s.Properties) > 0 {
		if b.Len() > 1 {
			b.WriteByte(',')
		}
		b.WriteString(`"properties":{`)
		for i, name := range s.propertyOrder {
			if i > 0 {
				b.WriteByte(',')
			}
			k, _ := json.Marshal(name)
			v, err := json.Marshal(s.Properties[name])
			if err != nil {
				return nil, err
			}
			b.Write(k)
			b.WriteByte(':')
			b.Write(v)
		}
		b.WriteByte('}')
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}
