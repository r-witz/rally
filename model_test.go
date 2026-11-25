package rally

import (
	"reflect"
	"testing"
)

func TestTags(t *testing.T) {
	text, number, list := reflect.TypeFor[string](), reflect.TypeFor[int](), reflect.TypeFor[[]struct{}]()
	parse := func(typ reflect.Type, tag string) (err any) {
		defer func() { err = recover() }()
		parseRules("T.F", reflect.StructTag(`json:"f" `+tag), typ, fieldTags)
		return nil
	}
	for typ, tags := range map[reflect.Type][]string{
		text: {
			`minLenght:"1"`, `maxlength:"1"`, `readonly:"true"`, `format:"email"`, `format:"uuid"`, `format:"date-time"`,
			`required:"true"`, `readOnly:"yes"`, `readOnly:"false"`, `deprecated:"true"`, `uniqueItems:"false"`, `patternDescription:"a name"`,
			`minLength:"-1"`, `maxLength:"x"`, `maximum:"3"`, `uniqueItems:"true"`, `doc:"a"`, `examples:"a"`,
		},
		number: {`exclusiveMinimum:"x"`, `multipleOf:"-2"`, `exclusiveMaximum:"+9"`, `minProperties:"1"`, `minimum:"low"`, `minimum:"1."`, `enum:"1,x"`, `enum:"1.5"`, `pattern:"^a"`, `minItems:"1"`},
		list:   {`uniqueItems:"true"`},
	} {
		for _, tag := range tags {
			if parse(typ, tag) == nil {
				t.Errorf("%s on %s: no panic", tag, typ)
			}
		}
	}
	for _, tag := range []string{
		`db:"n" gorm:"column:n" form:"n" env:"N" enums:"A,B" validate:"required" binding:"required"`, `description:"a name" example:"bob"`,
		`pattern:"^a"`,
	} {
		if err := parse(text, tag); err != nil {
			t.Errorf("%s: %v", tag, err)
		}
	}
}
