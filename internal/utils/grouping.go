// internal/utils/grouping.go
package utils

import (
	"sort"
	"strings"
)

// Route represents a route from the data source
type Route struct {
	Name          string
	Verb          string
	Path          string
	Gateway       string // Renamed from Controller - determines API Gateway
	Lambda        string // Renamed from Action - determines Lambda function
	Auth          string
	Tables        []string
	RequestModel    string // Name of the request model (from schema.tf.rb)
	ResponseModel   string // Name of the response model (from schema.tf.rb)
	ResponseContext string // Explicit context override for response model; empty = use Gateway
}

// ModelDefinition represents a JSON Schema model from schema.tf.rb
type ModelDefinition struct {
	Name        string                       `json:"name"`
	Description string                       `json:"description"`
	Properties  map[string]ModelProperty      `json:"properties"`
	Required    []string                     `json:"required"`
}

// ModelProperty represents a single property within a model
type ModelProperty struct {
	Type        string                       `json:"type"`
	Format      string                       `json:"format,omitempty"`
	Description string                       `json:"description,omitempty"`
	Enum        []string                     `json:"enum,omitempty"`
	MaxLength   int                          `json:"max_length,omitempty"`
	MinLength   int                          `json:"min_length,omitempty"`
	Items       *ModelProperty               `json:"items,omitempty"`
	Properties  map[string]ModelProperty      `json:"properties,omitempty"`
	Required    []string                     `json:"required,omitempty"`
}

// SortRoutes sorts routes deterministically for consistent hashing.
// Routes are sorted by: Gateway, Path, Verb, Lambda, Auth, Name.
// This ensures hash stability across plan and apply phases.
func SortRoutes(routes []Route) {
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Gateway != routes[j].Gateway {
			return routes[i].Gateway < routes[j].Gateway
		}
		if routes[i].Path != routes[j].Path {
			return routes[i].Path < routes[j].Path
		}
		if routes[i].Verb != routes[j].Verb {
			return routes[i].Verb < routes[j].Verb
		}
		if routes[i].Lambda != routes[j].Lambda {
			return routes[i].Lambda < routes[j].Lambda
		}
		if routes[i].Auth != routes[j].Auth {
			return routes[i].Auth < routes[j].Auth
		}
		return routes[i].Name < routes[j].Name
	})

	// Normalize and sort tables within each route for determinism
	// CRITICAL: Ensure nil slices become empty slices ([] not null in JSON)
	for i := range routes {
		if routes[i].Tables == nil {
			routes[i].Tables = []string{}
		} else if len(routes[i].Tables) > 0 {
			sort.Strings(routes[i].Tables)
		}
	}
}

// GroupByGateway groups routes by gateway (API Gateway)
// Renamed from GroupByController
func GroupByGateway(routes []Route) map[string][]Route {
	groups := make(map[string][]Route)
	for _, route := range routes {
		groups[route.Gateway] = append(groups[route.Gateway], route)
	}
	return groups
}

// GroupByLambda groups routes by lambda function
// Renamed from GroupByAction
func GroupByLambda(routes []Route) map[string][]Route {
	groups := make(map[string][]Route)
	for _, route := range routes {
		groups[route.Lambda] = append(groups[route.Lambda], route)
	}
	return groups
}

// GetTablesForLambda returns unique tables for a Lambda function
// Renamed from GetTablesForAction
func GetTablesForLambda(routes []Route, lambda string) []string {
	tableSet := make(map[string]bool)
	for _, route := range routes {
		if route.Lambda == lambda {
			for _, table := range route.Tables {
				if table != "" {
					tableSet[table] = true
				}
			}
		}
	}

	tables := make([]string, 0, len(tableSet))
	for table := range tableSet {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	return tables
}

// GetMethodsForPath returns all HTTP methods for a resource path (for OPTIONS CORS)
func GetMethodsForPath(routes []Route, path string) []string {
	methodSet := make(map[string]bool)
	for _, route := range routes {
		if route.Path == path {
			methodSet[route.Verb] = true
		}
	}

	methods := make([]string, 0, len(methodSet))
	for method := range methodSet {
		methods = append(methods, method)
	}
	sort.Strings(methods)
	return methods
}

// GetUniqueResourcePaths returns all unique resource paths from routes
func GetUniqueResourcePaths(routes []Route) []string {
	pathSet := make(map[string]bool)
	for _, route := range routes {
		pathSet[route.Path] = true
	}

	paths := make([]string, 0, len(pathSet))
	for path := range pathSet {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

// ParsePathSegments splits a path into segments for resource hierarchy creation
// Example: "/customer/pickups/{pickupId}" → ["customer", "pickups", "{pickupId}"]
func ParsePathSegments(path string) []string {
	// Remove leading slash and split
	path = strings.TrimPrefix(path, "/")
	if path == "" {
		return []string{}
	}
	return strings.Split(path, "/")
}

// BuildResourceHierarchy creates a map of resource paths and their parent relationships
// Returns map[resourcePath]parentPath
func BuildResourceHierarchy(routes []Route) map[string]string {
	hierarchy := make(map[string]string)

	for _, route := range routes {
		segments := ParsePathSegments(route.Path)
		currentPath := ""

		for i, segment := range segments {
			parentPath := currentPath
			if currentPath == "" {
				currentPath = "/" + segment
			} else {
				currentPath = currentPath + "/" + segment
			}

			// For the first segment (gateway), parent is root "/"
			if i == 0 {
				if _, exists := hierarchy[currentPath]; !exists {
					hierarchy[currentPath] = "/"
				}
			} else {
				// For subsequent segments, parent is the previous path
				if _, exists := hierarchy[currentPath]; !exists {
					hierarchy[currentPath] = parentPath
				}
			}
		}
	}

	return hierarchy
}

// GetGatewaysWithAuth returns gateways that have routes requiring Cognito auth
// Renamed from GetControllersWithAuth
func GetGatewaysWithAuth(routes []Route) []string {
	gatewaySet := make(map[string]bool)
	for _, route := range routes {
		if route.Auth == "cognito" {
			gatewaySet[route.Gateway] = true
		}
	}

	gateways := make([]string, 0, len(gatewaySet))
	for gateway := range gatewaySet {
		gateways = append(gateways, gateway)
	}
	sort.Strings(gateways)
	return gateways
}

// GetUniqueLambdas returns all unique lambda names
// Renamed from GetUniqueActions
func GetUniqueLambdas(routes []Route) []string {
	lambdaSet := make(map[string]bool)
	for _, route := range routes {
		lambdaSet[route.Lambda] = true
	}

	lambdas := make([]string, 0, len(lambdaSet))
	for lambda := range lambdaSet {
		lambdas = append(lambdas, lambda)
	}
	sort.Strings(lambdas)
	return lambdas
}

// GetUniqueGateways returns all unique gateway names
// Renamed from GetUniqueControllers
func GetUniqueGateways(routes []Route) []string {
	gatewaySet := make(map[string]bool)
	for _, route := range routes {
		gatewaySet[route.Gateway] = true
	}

	gateways := make([]string, 0, len(gatewaySet))
	for gateway := range gatewaySet {
		gateways = append(gateways, gateway)
	}
	sort.Strings(gateways)
	return gateways
}
