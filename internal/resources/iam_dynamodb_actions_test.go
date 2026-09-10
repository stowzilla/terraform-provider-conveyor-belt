// internal/resources/iam_dynamodb_actions_test.go
package resources

import "testing"

// actionsOf pulls the "Action" slice out of a policy statement.
func actionsOf(t *testing.T, stmt map[string]interface{}) []string {
	t.Helper()
	actions, ok := stmt["Action"].([]string)
	if !ok {
		t.Fatalf("statement Action is not []string: %#v", stmt["Action"])
	}
	return actions
}

func containsAction(actions []string, want string) bool {
	for _, a := range actions {
		if a == want {
			return true
		}
	}
	return false
}

// The bug this guards: ActiveItem loads multi-key finds and has_many :through with
// DynamoDB BatchGetItem, and batches writes with BatchWriteItem. The generated policy
// used to grant only single-item ops, so those paths 500'd with AccessDenied at runtime
// even though GetItem/Query/Scan worked. The read-write and read-only action sets must
// include the batch ops.
func TestDynamoDBReadWriteActionsIncludeBatchOps(t *testing.T) {
	for _, want := range []string{
		"dynamodb:GetItem",
		"dynamodb:BatchGetItem",
		"dynamodb:PutItem",
		"dynamodb:UpdateItem",
		"dynamodb:DeleteItem",
		"dynamodb:BatchWriteItem",
		"dynamodb:Query",
		"dynamodb:Scan",
	} {
		if !containsAction(dynamoReadWriteActions, want) {
			t.Errorf("read-write action set missing %q; got %v", want, dynamoReadWriteActions)
		}
	}
}

func TestDynamoDBReadOnlyActionsIncludeBatchGetButNoWrites(t *testing.T) {
	if !containsAction(dynamoReadOnlyActions, "dynamodb:BatchGetItem") {
		t.Errorf("read-only action set missing dynamodb:BatchGetItem; got %v", dynamoReadOnlyActions)
	}
	// Read-only must never grant a write, batch or otherwise.
	for _, forbidden := range []string{
		"dynamodb:PutItem",
		"dynamodb:UpdateItem",
		"dynamodb:DeleteItem",
		"dynamodb:BatchWriteItem",
	} {
		if containsAction(dynamoReadOnlyActions, forbidden) {
			t.Errorf("read-only action set must not grant %q; got %v", forbidden, dynamoReadOnlyActions)
		}
	}
}

func TestBuildDynamoDBPolicyStatements(t *testing.T) {
	rwArns := []string{"arn:aws:dynamodb:us-east-1:123:table/app-env-projects"}
	roArns := []string{"arn:aws:dynamodb:us-east-1:123:table/app-env-audit"}
	custom := map[string][]string{
		"dynamodb:BatchGetItem": {"arn:aws:dynamodb:us-east-1:123:table/app-env-users"},
	}

	stmts := buildDynamoDBPolicyStatements(rwArns, roArns, custom)
	if len(stmts) != 3 {
		t.Fatalf("expected 3 statements (rw, ro, custom), got %d: %#v", len(stmts), stmts)
	}

	// First statement is read-write and must carry BatchGetItem for the projects table.
	rw := stmts[0]
	if got := rw["Resource"]; !equalStringSlice(got.([]string), rwArns) {
		t.Errorf("read-write statement resources = %v, want %v", got, rwArns)
	}
	if !containsAction(actionsOf(t, rw), "dynamodb:BatchGetItem") {
		t.Errorf("read-write statement missing dynamodb:BatchGetItem")
	}

	// Second statement is read-only.
	if !containsAction(actionsOf(t, stmts[1]), "dynamodb:BatchGetItem") {
		t.Errorf("read-only statement missing dynamodb:BatchGetItem")
	}
}

func TestBuildDynamoDBPolicyStatementsEmpty(t *testing.T) {
	if stmts := buildDynamoDBPolicyStatements(nil, nil, nil); len(stmts) != 0 {
		t.Errorf("expected no statements for no ARNs, got %#v", stmts)
	}
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
