package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
	"github.com/mitchellh/mapstructure"
)

type Option struct {
	Name        string
	Description string
	Value       string
	Icon        string
}

type Validation struct {
	Min       int
	Max       int
	Monotonic string

	Regex string
	Error string
}

const (
	ValidationMonotonicIncreasing = "increasing"
	ValidationMonotonicDecreasing = "decreasing"
)

type Parameter struct {
	Value       string
	Name        string
	Description string
	Type        string
	Mutable     bool
	Default     string
	Icon        string
	Option      []Option
	Validation  []Validation
	Optional    bool

	LegacyVariableName string
	LegacyVariable     string
}

func parameterDataSource() *schema.Resource {
	return &schema.Resource{
		Description: "Use this data source to configure editable options for workspaces.",
		ReadContext: func(ctx context.Context, rd *schema.ResourceData, i interface{}) diag.Diagnostics {
			rd.SetId(uuid.NewString())

			var parameter Parameter
			err := mapstructure.Decode(struct {
				Value       interface{}
				Name        interface{}
				Description interface{}
				Type        interface{}
				Mutable     interface{}
				Default     interface{}
				Icon        interface{}
				Option      interface{}
				Validation  interface{}
				Optional    interface{}

				LegacyVariableName interface{}
				LegacyVariable     interface{}
			}{
				Value:       rd.Get("value"),
				Name:        rd.Get("name"),
				Description: rd.Get("description"),
				Type:        rd.Get("type"),
				Mutable:     rd.Get("mutable"),
				Default: func() interface{} {
					standardMode := rd.GetRawConfig().AsValueMap()["legacy_variable"].IsNull()
					if standardMode {
						return rd.Get("default")
					}

					// legacy variable is linked
					legacyVariable := rd.GetRawConfig().AsValueMap()["legacy_variable"].AsString()
					rd.Set("default", legacyVariable)
					return legacyVariable
				}(),
				Icon:       rd.Get("icon"),
				Option:     rd.Get("option"),
				Validation: rd.Get("validation"),
				Optional: func() bool {
					// This hack allows for checking if the "default" field is present in the .tf file.
					// If "default" is missing or is "null", then it means that this field is required,
					// and user must provide a value for it.
					val := !rd.GetRawConfig().AsValueMap()["default"].IsNull()
					rd.Set("optional", val)
					return val
				}(),
				LegacyVariableName: rd.Get("legacy_variable_name"),
				LegacyVariable:     rd.Get("legacy_variable"),
			}, &parameter)
			if err != nil {
				return diag.Errorf("decode parameter: %s", err)
			}
			var value string
			if parameter.Default != "" {
				err := valueIsType(parameter.Type, parameter.Default)
				if err != nil {
					return err
				}
				value = parameter.Default
			}
			envValue, ok := os.LookupEnv(ParameterEnvironmentVariable(parameter.Name))
			if ok {
				value = envValue
			}
			rd.Set("value", value)

			if len(parameter.Validation) == 1 {
				validation := &parameter.Validation[0]
				err = validation.Valid(parameter.Type, value)
				if err != nil {
					return diag.FromErr(err)
				}
			}

			if len(parameter.Option) > 0 {
				names := map[string]interface{}{}
				values := map[string]interface{}{}
				for _, option := range parameter.Option {
					_, exists := names[option.Name]
					if exists {
						return diag.Errorf("multiple options cannot have the same name %q", option.Name)
					}
					_, exists = values[option.Value]
					if exists {
						return diag.Errorf("multiple options cannot have the same value %q", option.Value)
					}
					err := valueIsType(parameter.Type, option.Value)
					if err != nil {
						return err
					}
					values[option.Value] = nil
					names[option.Name] = nil
				}

				if parameter.Default != "" {
					_, defaultIsValid := values[parameter.Default]
					if !defaultIsValid {
						return diag.Errorf("default value %q must be defined as one of options", parameter.Default)
					}
				}
			}
			return nil
		},
		Schema: map[string]*schema.Schema{
			"value": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "The output value of the parameter.",
			},
			"name": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "The name of the parameter as it will appear in the interface. If this is changed, developers will be re-prompted for a new value.",
			},
			"description": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Describe what this parameter does.",
			},
			"type": {
				Type:         schema.TypeString,
				Default:      "string",
				Optional:     true,
				ValidateFunc: validation.StringInSlice([]string{"number", "string", "bool"}, false),
				Description:  `The type of this parameter. Must be one of: "number", "string", or "bool".`,
			},
			"mutable": {
				Type:        schema.TypeBool,
				Optional:    true,
				Default:     false,
				Description: "Whether this value can be changed after workspace creation. This can be destructive for values like region, so use with caution!",
			},
			"default": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "A default value for the parameter.",
			},
			"icon": {
				Type: schema.TypeString,
				Description: "A URL to an icon that will display in the dashboard. View built-in " +
					"icons here: https://github.com/coder/coder/tree/main/site/static/icon. Use a " +
					"built-in icon with `data.coder_workspace.me.access_url + \"/icon/<path>\"`.",
				ForceNew: true,
				Optional: true,
				ValidateFunc: func(i interface{}, s string) ([]string, []error) {
					_, err := url.Parse(s)
					if err != nil {
						return nil, []error{err}
					}
					return nil, nil
				},
			},
			"option": {
				Type:          schema.TypeList,
				Description:   "Each \"option\" block defines a value for a user to select from.",
				ForceNew:      true,
				Optional:      true,
				MaxItems:      64,
				ConflictsWith: []string{"validation"},
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"name": {
							Type:        schema.TypeString,
							Description: "The display name of this value in the UI.",
							ForceNew:    true,
							Required:    true,
						},
						"description": {
							Type:        schema.TypeString,
							Description: "Describe what selecting this value does.",
							ForceNew:    true,
							Optional:    true,
						},
						"value": {
							Type:        schema.TypeString,
							Description: "The value of this option set on the parameter if selected.",
							ForceNew:    true,
							Required:    true,
						},
						"icon": {
							Type: schema.TypeString,
							Description: "A URL to an icon that will display in the dashboard. View built-in " +
								"icons here: https://github.com/coder/coder/tree/main/site/static/icon. Use a " +
								"built-in icon with `data.coder_workspace.me.access_url + \"/icon/<path>\"`.",
							ForceNew: true,
							Optional: true,
							ValidateFunc: func(i interface{}, s string) ([]string, []error) {
								_, err := url.Parse(s)
								if err != nil {
									return nil, []error{err}
								}
								return nil, nil
							},
						},
					},
				},
			},
			"validation": {
				Type:          schema.TypeList,
				MaxItems:      1,
				Optional:      true,
				Description:   "Validate the input of a parameter.",
				ConflictsWith: []string{"option"},
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"min": {
							Type:         schema.TypeInt,
							Optional:     true,
							Default:      0,
							Description:  "The minimum of a number parameter.",
							RequiredWith: []string{"validation.0.max"},
						},
						"max": {
							Type:         schema.TypeInt,
							Optional:     true,
							Description:  "The maximum of a number parameter.",
							RequiredWith: []string{"validation.0.min"},
						},
						"monotonic": {
							Type:        schema.TypeString,
							Optional:    true,
							Description: "Number monotonicity, either increasing or decreasing.",
						},
						"regex": {
							Type:          schema.TypeString,
							ConflictsWith: []string{"validation.0.min", "validation.0.max", "validation.0.monotonic"},
							Description:   "A regex for the input parameter to match against.",
							Optional:      true,
						},
						"error": {
							Type:         schema.TypeString,
							Optional:     true,
							RequiredWith: []string{"validation.0.regex"},
							Description:  "An error message to display if the value doesn't match the provided regex.",
						},
					},
				},
			},
			"optional": {
				Type:        schema.TypeBool,
				Computed:    true,
				Description: "Whether this value is optional.",
			},
			"legacy_variable_name": {
				Type:         schema.TypeString,
				Optional:     true,
				RequiredWith: []string{"legacy_variable"},
				Description:  "Name of the legacy Terraform variable. Coder will use it to lookup the variable value.",
			},
			"legacy_variable": {
				Type:         schema.TypeString,
				Optional:     true,
				RequiredWith: []string{"legacy_variable_name"},
				Description:  "Reference to the Terraform variable. Coder will use it to lookup the default value.",
			},
		},
	}
}

func valueIsType(typ, value string) diag.Diagnostics {
	switch typ {
	case "number":
		_, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return diag.Errorf("%q is not a number", value)
		}
	case "bool":
		_, err := strconv.ParseBool(value)
		if err != nil {
			return diag.Errorf("%q is not a bool", value)
		}
	case "string":
		// Anything is a string!
	default:
		return diag.Errorf("invalid type %q", typ)
	}
	return nil
}

func (v *Validation) Valid(typ, value string) error {
	if typ != "number" {
		if v.Min != 0 {
			return fmt.Errorf("a min cannot be specified for a %s type", typ)
		}
		if v.Max != 0 {
			return fmt.Errorf("a max cannot be specified for a %s type", typ)
		}
	}
	if typ != "string" && v.Regex != "" {
		return fmt.Errorf("a regex cannot be specified for a %s type", typ)
	}
	switch typ {
	case "bool":
		if value != "true" && value != "false" {
			return fmt.Errorf(`boolean value can be either "true" or "false"`)
		}
		return nil
	case "string":
		if v.Regex == "" {
			return nil
		}
		regex, err := regexp.Compile(v.Regex)
		if err != nil {
			return fmt.Errorf("compile regex %q: %s", regex, err)
		}
		if v.Error == "" {
			return fmt.Errorf("an error must be specified with a regex validation")
		}
		matched := regex.MatchString(value)
		if !matched {
			return fmt.Errorf("%s (value %q does not match %q)", v.Error, value, regex)
		}
	case "number":
		num, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("value %q is not a number", value)
		}
		if num < v.Min {
			return fmt.Errorf("value %d is less than the minimum %d", num, v.Min)
		}
		if num > v.Max {
			return fmt.Errorf("value %d is more than the maximum %d", num, v.Max)
		}
		if v.Monotonic != "" && v.Monotonic != ValidationMonotonicIncreasing && v.Monotonic != ValidationMonotonicDecreasing {
			return fmt.Errorf("number monotonicity can be either %q or %q", ValidationMonotonicIncreasing, ValidationMonotonicDecreasing)
		}
	}
	return nil
}

// ParameterEnvironmentVariable returns the environment variable to specify for
// a parameter by it's name. It's hashed because spaces and special characters
// can be used in parameter names that may not be valid in env vars.
func ParameterEnvironmentVariable(name string) string {
	sum := sha256.Sum256([]byte(name))
	return "CODER_PARAMETER_" + hex.EncodeToString(sum[:])
}
