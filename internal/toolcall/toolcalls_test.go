package toolcall

import (
	"strings"
	"testing"
)

func TestFormatOpenAIToolCalls(t *testing.T) {
	formatted := FormatOpenAIToolCalls([]ParsedToolCall{{Name: "search", Input: map[string]any{"q": "x"}}}, nil)
	if len(formatted) != 1 {
		t.Fatalf("expected 1, got %d", len(formatted))
	}
	fn, _ := formatted[0]["function"].(map[string]any)
	if fn["name"] != "search" {
		t.Fatalf("unexpected function name: %#v", fn)
	}
}

func TestParseToolCallsSupportsToolCallsWrapper(t *testing.T) {
	text := `<tool_calls><invoke name="Bash"><parameter name="command">pwd</parameter><parameter name="description">show cwd</parameter></invoke></tool_calls>`
	calls := ParseToolCalls(text, []string{"bash"})
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %#v", calls)
	}
	if calls[0].Name != "Bash" {
		t.Fatalf("expected original tool name Bash, got %q", calls[0].Name)
	}
	if calls[0].Input["command"] != "pwd" {
		t.Fatalf("expected command argument, got %#v", calls[0].Input)
	}
}

func TestParseToolCallsSupportsDSMLShell(t *testing.T) {
	text := `<|DSML|tool_calls><|DSML|invoke name="Bash"><|DSML|parameter name="command"><![CDATA[pwd]]></|DSML|parameter></|DSML|invoke></|DSML|tool_calls>`
	calls := ParseToolCalls(text, []string{"Bash"})
	if len(calls) != 1 {
		t.Fatalf("expected 1 DSML call, got %#v", calls)
	}
	if calls[0].Name != "Bash" || calls[0].Input["command"] != "pwd" {
		t.Fatalf("unexpected DSML parse result: %#v", calls[0])
	}
}

// TestParseToolCallsSupportsChineseWrapper covers a real-world model emission
// where the wrapper tag uses the Chinese name ("工具调用") with a single-pipe
// closing. Without normalization the rest of the pipeline would never see a
// canonical "<tool_calls>" and the markup would leak to the client as text.
func TestParseToolCallsSupportsChineseWrapper(t *testing.T) {
	text := `<|DSML工具调用|><|DSML|invoke name="Bash"><|DSML|parameter name="command"><![CDATA[curl https://example.com/healthz]]></|DSML|parameter></|DSML|invoke></|DSML|tool_calls>`
	calls := ParseToolCalls(text, []string{"Bash"})
	if len(calls) != 1 {
		t.Fatalf("expected 1 call from Chinese-wrapper input, got %#v", calls)
	}
	if calls[0].Name != "Bash" || calls[0].Input["command"] != "curl https://example.com/healthz" {
		t.Fatalf("unexpected parse: %#v", calls[0])
	}
}

// TestParseToolCallsSupportsSinglePipeShorthand covers a malformed emission
// where the model omits the second pipe between "DSML" and the tag name
// ("<|DSMLtool_calls>" / "<|DSMLinvoke ..."). Detection + canonicalization
// must still extract the call.
func TestParseToolCallsSupportsSinglePipeShorthand(t *testing.T) {
	text := `<|DSMLtool_calls><|DSMLinvoke name="Bash"><|DSMLparameter name="command"><![CDATA[ls /tmp]]></|DSMLparameter></|DSMLinvoke></|DSMLtool_calls>`
	calls := ParseToolCalls(text, []string{"Bash"})
	if len(calls) != 1 {
		t.Fatalf("expected 1 call from single-pipe shorthand, got %#v", calls)
	}
	if calls[0].Name != "Bash" || calls[0].Input["command"] != "ls /tmp" {
		t.Fatalf("unexpected parse: %#v", calls[0])
	}
}

// TestParseToolCallsRecoversFromMalformedCDATAClose covers a real DeepSeek
// regression where every CDATA section closes with "]]<TAG" instead of the
// canonical "]]><TAG". Before the pre-normalize sanitize pass, the rewriter
// would bail at the first malformed CDATA and the entire block leaked through
// as text, causing Claude Code to treat the tool call as a normal assistant
// message and silently end the turn.
func TestParseToolCallsRecoversFromMalformedCDATAClose(t *testing.T) {
	text := `<|DSMLtool_calls><|DSMLinvoke name="Bash"><|DSMLparameter name="command"><![CDATA[curl -fskS https://10.3.3.14:8588/healthz 2>&1]]</|DSMLparameter><|DSMLparameter name="description"><![CDATA[Test HTTPS]]</|DSMLparameter><|DSMLparameter name="timeout"><![CDATA[15000]]</|DSMLparameter></|DSMLinvoke><|DSMLinvoke name="Bash"><|DSMLparameter name="command"><![CDATA[curl -fsS http://10.3.3.14:8580/healthz 2>&1]]</|DSMLparameter></|DSMLinvoke></|DSMLtool_calls>`
	calls := ParseToolCalls(text, []string{"Bash"})
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls recovered from malformed CDATA, got %d: %#v", len(calls), calls)
	}
	if calls[0].Input["command"] != "curl -fskS https://10.3.3.14:8588/healthz 2>&1" {
		t.Fatalf("unexpected first command: %q", calls[0].Input["command"])
	}
	if calls[1].Input["command"] != "curl -fsS http://10.3.3.14:8580/healthz 2>&1" {
		t.Fatalf("unexpected second command: %q", calls[1].Input["command"])
	}
}

// TestParseToolCallsSupportsTrailingPipeBeforeClose covers wrappers that close
// with "|>" instead of plain ">", e.g. "<|DSML|tool_calls|>".
func TestParseToolCallsSupportsTrailingPipeBeforeClose(t *testing.T) {
	text := `<|DSML|tool_calls|><|DSML|invoke name="Bash"|><|DSML|parameter name="command"|><![CDATA[id]]></|DSML|parameter></|DSML|invoke></|DSML|tool_calls>`
	calls := ParseToolCalls(text, []string{"Bash"})
	if len(calls) != 1 {
		t.Fatalf("expected 1 call from trailing-pipe input, got %#v", calls)
	}
	if calls[0].Name != "Bash" || calls[0].Input["command"] != "id" {
		t.Fatalf("unexpected parse: %#v", calls[0])
	}
}

func TestParseToolCallsSupportsDSMLShellWithCanonicalExampleInCDATA(t *testing.T) {
	content := `<tool_calls><invoke name="demo"><parameter name="value">x</parameter></invoke></tool_calls>`
	text := `<|DSML|tool_calls><|DSML|invoke name="Write"><|DSML|parameter name="file_path">notes.md</|DSML|parameter><|DSML|parameter name="content"><![CDATA[` + content + `]]></|DSML|parameter></|DSML|invoke></|DSML|tool_calls>`
	calls := ParseToolCalls(text, []string{"Write"})
	if len(calls) != 1 {
		t.Fatalf("expected 1 DSML call with XML-looking CDATA, got %#v", calls)
	}
	if calls[0].Name != "Write" || calls[0].Input["content"] != content {
		t.Fatalf("unexpected DSML CDATA parse result: %#v", calls[0])
	}
}

func TestParseToolCallsPreservesSimpleCDATAInlineMarkupAsText(t *testing.T) {
	text := `<tool_calls><invoke name="Write"><parameter name="description"><![CDATA[<b>urgent</b>]]></parameter></invoke></tool_calls>`
	calls := ParseToolCalls(text, []string{"Write"})
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %#v", calls)
	}
	got, ok := calls[0].Input["description"].(string)
	if !ok {
		t.Fatalf("expected description to remain a string, got %#v", calls[0].Input["description"])
	}
	if got != "<b>urgent</b>" {
		t.Fatalf("expected inline markup CDATA to stay raw, got %q", got)
	}
}

func TestParseToolCallsTreatsUnclosedCDATAAsText(t *testing.T) {
	text := `<tool_calls><invoke name="Write"><parameter name="content"><![CDATA[hello world</parameter></invoke></tool_calls>`
	res := ParseToolCallsDetailed(text, []string{"Write"})
	if len(res.Calls) != 1 {
		t.Fatalf("expected unclosed CDATA to still parse via outer wrapper, got %#v", res.Calls)
	}
	got, _ := res.Calls[0].Input["content"].(string)
	if got != "hello world" {
		t.Fatalf("expected recovered CDATA payload, got %q", got)
	}
}

func TestParseToolCallsNormalizesMixedDSMLAndCanonicalToolTags(t *testing.T) {
	// Models commonly mix DSML wrapper tags with canonical inner tags.
	// These should be normalized and parsed, not rejected.
	text := `<|DSML|tool_calls><invoke name="Bash"><|DSML|parameter name="command">pwd</|DSML|parameter></invoke></|DSML|tool_calls>`
	calls := ParseToolCalls(text, []string{"Bash"})
	if len(calls) != 1 {
		t.Fatalf("expected mixed DSML/XML tool tags to be normalized and parsed, got %#v", calls)
	}
	if calls[0].Name != "Bash" || calls[0].Input["command"] != "pwd" {
		t.Fatalf("unexpected mixed DSML parse result: %#v", calls[0])
	}
}

func TestParseToolCallsSupportsStandaloneToolWithMultilineCDATAAndRepeatedXMLTags(t *testing.T) {
	text := `<tool_calls><invoke name="write_file"><parameter name="path">script.sh</parameter><parameter name="content"><![CDATA[#!/bin/bash
echo "hello"
]]></parameter><parameter name="item">first</parameter><parameter name="item">second</parameter></invoke></tool_calls>`
	calls := ParseToolCalls(text, []string{"write_file"})
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %#v", calls)
	}
	if calls[0].Name != "write_file" {
		t.Fatalf("expected tool name write_file, got %q", calls[0].Name)
	}
	if calls[0].Input["path"] != "script.sh" {
		t.Fatalf("expected path argument, got %#v", calls[0].Input)
	}
	content, _ := calls[0].Input["content"].(string)
	if !strings.Contains(content, "#!/bin/bash") || !strings.Contains(content, "echo \"hello\"") {
		t.Fatalf("expected multiline CDATA content to be preserved, got %#v", calls[0].Input["content"])
	}
	items, ok := calls[0].Input["item"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("expected repeated XML tags to become an array, got %#v", calls[0].Input["item"])
	}
}

func TestParseToolCallsKeepsToolSyntaxInsideCDATAAsParameterText(t *testing.T) {
	payload := strings.Join([]string{
		"# Release notes",
		"",
		"```xml",
		"<tool_calls>",
		"  <invoke name=\"demo\">",
		"    <parameter name=\"value\">x</parameter>",
		"  </invoke>",
		"</tool_calls>",
		"```",
	}, "\n")
	text := `<tool_calls><invoke name="Write"><parameter name="content"><![CDATA[` + payload + `]]></parameter><parameter name="file_path">DeepSeek_Web_To_API-4.0-Release-Notes.md</parameter></invoke></tool_calls>`
	calls := ParseToolCalls(text, []string{"Write"})
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %#v", calls)
	}
	content, _ := calls[0].Input["content"].(string)
	if content != payload {
		t.Fatalf("expected CDATA payload with nested tool syntax to survive intact, got %q", content)
	}
	if calls[0].Input["file_path"] != "DeepSeek_Web_To_API-4.0-Release-Notes.md" {
		t.Fatalf("expected file_path parameter, got %#v", calls[0].Input)
	}
}

func TestParseToolCallsSupportsInvokeParameters(t *testing.T) {
	text := `<tool_calls><invoke name="get_weather"><parameter name="city">beijing</parameter><parameter name="unit">c</parameter></invoke></tool_calls>`
	calls := ParseToolCalls(text, []string{"get_weather"})
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %#v", calls)
	}
	if calls[0].Name != "get_weather" {
		t.Fatalf("expected tool name get_weather, got %q", calls[0].Name)
	}
	if calls[0].Input["city"] != "beijing" || calls[0].Input["unit"] != "c" {
		t.Fatalf("expected parsed json parameters, got %#v", calls[0].Input)
	}
}

func TestParseToolCallsRejectsOnlyEmptyArguments(t *testing.T) {
	text := `<tool_calls><invoke name="Bash"><parameter name="command"></parameter><parameter name="cwd">   </parameter></invoke></tool_calls>`
	res := ParseToolCallsDetailed(text, []string{"Bash"})
	if len(res.Calls) != 0 {
		t.Fatalf("expected empty-argument call to be rejected, got %#v", res.Calls)
	}
	if !res.SawToolCallSyntax {
		t.Fatalf("expected syntax to be detected even when call is rejected")
	}
}

func TestParseToolCallsSupportsJSONScalarParameters(t *testing.T) {
	text := `<tool_calls><invoke name="configure"><parameter name="count">123</parameter><parameter name="max_tokens"><![CDATA[256]]></parameter><parameter name="enabled">true</parameter></invoke></tool_calls>`
	calls := ParseToolCalls(text, []string{"configure"})
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %#v", calls)
	}
	if got, ok := calls[0].Input["count"].(float64); !ok || got != 123 {
		t.Fatalf("expected numeric count, got %#v", calls[0].Input["count"])
	}
	if got, ok := calls[0].Input["max_tokens"].(float64); !ok || got != 256 {
		t.Fatalf("expected numeric max_tokens, got %#v", calls[0].Input["max_tokens"])
	}
	if got, ok := calls[0].Input["enabled"].(bool); !ok || !got {
		t.Fatalf("expected boolean enabled, got %#v", calls[0].Input["enabled"])
	}
}

func TestParseToolCallsTreatsItemOnlyParameterBodyAsArray(t *testing.T) {
	text := strings.Join([]string{
		`<|DSML|tool_calls>`,
		`<|DSML|invoke name="AskUserQuestion">`,
		`<|DSML|parameter name="questions">`,
		`<item>`,
		`<question><![CDATA[What would you like to do next?]]></question>`,
		`<header><![CDATA[Next step]]></header>`,
		`<options>`,
		`<item><label><![CDATA[Run tests]]></label><description><![CDATA[Run the test suite]]></description></item>`,
		`<item><label><![CDATA[Other task]]></label><description><![CDATA[Something else entirely]]></description></item>`,
		`</options>`,
		`<multiSelect>false</multiSelect>`,
		`</item>`,
		`</|DSML|parameter>`,
		`</|DSML|invoke>`,
		`</|DSML|tool_calls>`,
	}, "\n")
	calls := ParseToolCalls(text, []string{"AskUserQuestion"})
	if len(calls) != 1 {
		t.Fatalf("expected one AskUserQuestion call, got %#v", calls)
	}
	questions, ok := calls[0].Input["questions"].([]any)
	if !ok || len(questions) != 1 {
		t.Fatalf("expected questions to parse as array, got %#v", calls[0].Input["questions"])
	}
	first, ok := questions[0].(map[string]any)
	if !ok {
		t.Fatalf("expected first question object, got %#v", questions[0])
	}
	if first["question"] != "What would you like to do next?" || first["header"] != "Next step" || first["multiSelect"] != false {
		t.Fatalf("unexpected question payload: %#v", first)
	}
	options, ok := first["options"].([]any)
	if !ok || len(options) != 2 {
		t.Fatalf("expected options to parse as array, got %#v", first["options"])
	}
}

func TestParseToolCallsTreatsCDATAItemOnlyBodyAsArray(t *testing.T) {
	todos := `<br>  <item><br>    <activeForm>Testing EnterWorktree tool</activeForm><br>    <content>Test EnterWorktree tool</content><br>    <status>in_progress</status><br>  </item><br>  <item><br>    <activeForm>Testing TodoWrite tool</activeForm><br>    <content>Test TodoWrite tool</content><br>    <status>completed</status><br>  </item><br>`
	text := `<|DSML|tool_calls><|DSML|invoke name="TodoWrite"><|DSML|parameter name="todos"><![CDATA[` + todos + `]]></|DSML|parameter></|DSML|invoke></|DSML|tool_calls>`
	calls := ParseToolCalls(text, []string{"TodoWrite"})
	if len(calls) != 1 {
		t.Fatalf("expected one TodoWrite call, got %#v", calls)
	}
	items, ok := calls[0].Input["todos"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("expected todos CDATA item body to parse as array, got %#v", calls[0].Input["todos"])
	}
	first, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("expected first todo object, got %#v", items[0])
	}
	if first["activeForm"] != "Testing EnterWorktree tool" || first["content"] != "Test EnterWorktree tool" || first["status"] != "in_progress" {
		t.Fatalf("unexpected first todo: %#v", first)
	}
}

func TestParseToolCallsTreatsSingleItemCDATAAsArray(t *testing.T) {
	text := `<tool_calls><invoke name="TodoWrite"><parameter name="todos"><![CDATA[<item>one</item>]]></parameter></invoke></tool_calls>`
	calls := ParseToolCalls(text, []string{"TodoWrite"})
	if len(calls) != 1 {
		t.Fatalf("expected one TodoWrite call, got %#v", calls)
	}
	items, ok := calls[0].Input["todos"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("expected single-item CDATA body to parse as array, got %#v", calls[0].Input["todos"])
	}
	if got, ok := items[0].(string); !ok || got != "one" {
		t.Fatalf("expected single item value to stay intact, got %#v", items[0])
	}
}

func TestParseToolCallsTreatsLooseJSONListAsArray(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "plain text",
			body: `{"content":"Test TodoWrite tool","status":"completed"}, {"content":"Another task","status":"pending"}`,
		},
		{
			name: "cdata",
			body: `<![CDATA[{"content":"Test TodoWrite tool","status":"completed"}, {"content":"Another task","status":"pending"}]]>`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text := `<tool_calls><invoke name="TodoWrite"><parameter name="todos">` + tt.body + `</parameter></invoke></tool_calls>`
			calls := ParseToolCalls(text, []string{"TodoWrite"})
			if len(calls) != 1 {
				t.Fatalf("expected one TodoWrite call, got %#v", calls)
			}
			items, ok := calls[0].Input["todos"].([]any)
			if !ok || len(items) != 2 {
				t.Fatalf("expected loose JSON list to parse as array, got %#v", calls[0].Input["todos"])
			}
			first, ok := items[0].(map[string]any)
			if !ok {
				t.Fatalf("expected first todo object, got %#v", items[0])
			}
			if first["content"] != "Test TodoWrite tool" || first["status"] != "completed" {
				t.Fatalf("unexpected first todo: %#v", first)
			}
		})
	}
}

func TestParseToolCallsKeepsPreservedTextParametersAsText(t *testing.T) {
	text := `<tool_calls><invoke name="Write"><parameter name="content"><![CDATA[{"content":"Test TodoWrite tool","status":"completed"}, {"content":"Another task","status":"pending"}]]></parameter></invoke></tool_calls>`
	calls := ParseToolCalls(text, []string{"Write"})
	if len(calls) != 1 {
		t.Fatalf("expected one Write call, got %#v", calls)
	}
	got, ok := calls[0].Input["content"].(string)
	if !ok {
		t.Fatalf("expected content to stay a string, got %#v", calls[0].Input["content"])
	}
	want := `{"content":"Test TodoWrite tool","status":"completed"}, {"content":"Another task","status":"pending"}`
	if got != want {
		t.Fatalf("expected content to stay raw, got %q", got)
	}
}

func TestParseToolCallsTreatsCDATAObjectFragmentAsObject(t *testing.T) {
	payload := `<question><![CDATA[Pick one]]></question><options><item><label><![CDATA[A]]></label></item><item><label><![CDATA[B]]></label></item></options>`
	text := `<tool_calls><invoke name="AskUserQuestion"><parameter name="questions"><![CDATA[` + payload + `]]></parameter></invoke></tool_calls>`
	calls := ParseToolCalls(text, []string{"AskUserQuestion"})
	if len(calls) != 1 {
		t.Fatalf("expected one AskUserQuestion call, got %#v", calls)
	}
	question, ok := calls[0].Input["questions"].(map[string]any)
	if !ok {
		t.Fatalf("expected CDATA XML object fragment to parse as object, got %#v", calls[0].Input["questions"])
	}
	options, ok := question["options"].([]any)
	if question["question"] != "Pick one" || !ok || len(options) != 2 {
		t.Fatalf("unexpected parsed question: %#v", question)
	}
}

func TestParseToolCallsPreservesRawMalformedParams(t *testing.T) {
	text := `<tool_calls><invoke name="execute_command"><parameter name="command">cd /root && git status</parameter></invoke></tool_calls>`
	calls := ParseToolCalls(text, []string{"execute_command"})
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %#v", calls)
	}
	if calls[0].Name != "execute_command" {
		t.Fatalf("expected tool name execute_command, got %q", calls[0].Name)
	}
	raw, ok := calls[0].Input["command"].(string)
	if !ok {
		t.Fatalf("expected raw command tracking, got %#v", calls[0].Input)
	}
	if raw != "cd /root && git status" {
		t.Fatalf("expected raw arguments to be preserved, got %q", raw)
	}
}

func TestParseToolCallsSupportsParamsJSONWithAmpersandCommand(t *testing.T) {
	text := `<tool_calls><invoke name="execute_command"><parameter name="command">sshpass -p 'xxx' ssh -o StrictHostKeyChecking=no -p 1111 root@111.111.111.111 'cd /root && git clone https://github.com/ericc-ch/copilot-api.git'</parameter><parameter name="cwd"></parameter><parameter name="timeout"></parameter></invoke></tool_calls>`
	calls := ParseToolCalls(text, []string{"execute_command"})
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %#v", calls)
	}
	if calls[0].Name != "execute_command" {
		t.Fatalf("expected tool name execute_command, got %q", calls[0].Name)
	}
	cmd, _ := calls[0].Input["command"].(string)
	if !strings.Contains(cmd, "&& git clone") {
		t.Fatalf("expected command to keep && segment, got %#v", calls[0].Input)
	}
}

func TestParseToolCallsDoesNotTreatParamsNameTagAsToolName(t *testing.T) {
	text := `<tool_calls><invoke name="execute_command"><parameter name="tool_name">file.txt</parameter><parameter name="command">pwd</parameter></invoke></tool_calls>`
	calls := ParseToolCalls(text, []string{"execute_command"})
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %#v", calls)
	}
	if calls[0].Name != "execute_command" {
		t.Fatalf("expected tool name execute_command, got %q", calls[0].Name)
	}
	if calls[0].Input["tool_name"] != "file.txt" {
		t.Fatalf("expected parameter name preserved, got %#v", calls[0].Input)
	}
}

func TestParseToolCallsDetailedMarksToolCallsSyntax(t *testing.T) {
	text := `<tool_calls><invoke name="Bash"><parameter name="command">pwd</parameter></invoke></tool_calls>`
	res := ParseToolCallsDetailed(text, []string{"bash"})
	if !res.SawToolCallSyntax {
		t.Fatalf("expected SawToolCallSyntax=true, got %#v", res)
	}
	if len(res.Calls) != 1 {
		t.Fatalf("expected one parsed call, got %#v", res)
	}
}

func TestParseToolCallsRejectsAllEmptyParameterPayload(t *testing.T) {
	text := `<tool_calls><invoke name="Bash"><parameter name="command"></parameter><parameter name="description">   </parameter><parameter name="timeout"></parameter></invoke></tool_calls>`
	res := ParseToolCallsDetailed(text, []string{"Bash"})
	if !res.SawToolCallSyntax {
		t.Fatalf("expected tool syntax to be detected, got %#v", res)
	}
	if len(res.Calls) != 0 {
		t.Fatalf("expected all-empty payload to be rejected, got %#v", res.Calls)
	}
}

func TestParseToolCallsPreservesExplicitZeroArgToolCall(t *testing.T) {
	text := `<tool_calls><invoke name="noop"></invoke></tool_calls>`
	res := ParseToolCallsDetailed(text, []string{"noop"})
	if len(res.Calls) != 1 {
		t.Fatalf("expected zero-arg tool call to remain valid, got %#v", res.Calls)
	}
	if len(res.Calls[0].Input) != 0 {
		t.Fatalf("expected empty input map for zero-arg tool call, got %#v", res.Calls[0].Input)
	}
}

func TestParseToolCallsSupportsInlineJSONToolObject(t *testing.T) {
	text := `<tool_calls><invoke name="Bash">{"input":{"command":"pwd","description":"show cwd"}}</invoke></tool_calls>`
	calls := ParseToolCalls(text, []string{"bash"})
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %#v", calls)
	}
	if calls[0].Name != "Bash" {
		t.Fatalf("expected original tool name Bash, got %q", calls[0].Name)
	}
	if calls[0].Input["command"] != "pwd" {
		t.Fatalf("expected command argument, got %#v", calls[0].Input)
	}
}

func TestParseToolCallsDoesNotAcceptMismatchedMarkupTags(t *testing.T) {
	text := `<tool_calls><invoke name="read_file"><parameter name="path">README.md</function></invoke></tool_calls>`
	calls := ParseToolCalls(text, []string{"read_file"})
	if len(calls) != 0 {
		t.Fatalf("expected mismatched tags to be rejected, got %#v", calls)
	}
}

func TestParseToolCallsDoesNotTreatNameInsideParamsAsToolName(t *testing.T) {
	text := `<tool_calls><invoke><parameter name="path">README.md</parameter></invoke></tool_calls>`
	calls := ParseToolCalls(text, []string{"read_file"})
	if len(calls) != 0 {
		t.Fatalf("expected no tool call when name appears only under params, got %#v", calls)
	}
}

func TestParseToolCallsRejectsLegacyToolsWrapper(t *testing.T) {
	text := `<tools><tool_call><tool_name>read_file</tool_name><param>{"path":"README.md"}</param></tool_call></tools>`
	calls := ParseToolCalls(text, []string{"read_file"})
	if len(calls) != 0 {
		t.Fatalf("expected legacy tools wrapper to be rejected, got %#v", calls)
	}
}

func TestParseToolCallsRejectsBareInvokeWithoutToolCallsWrapper(t *testing.T) {
	text := `<invoke name="read_file"><parameter name="path">README.md</parameter></invoke>`
	res := ParseToolCallsDetailed(text, []string{"read_file"})
	if len(res.Calls) != 0 {
		t.Fatalf("expected bare invoke to be rejected, got %#v", res.Calls)
	}
	if res.SawToolCallSyntax {
		t.Fatalf("expected bare invoke to no longer count as supported syntax, got %#v", res)
	}
}

func TestParseToolCallsRepairsMissingOpeningToolCallsWrapperWhenClosingTagExists(t *testing.T) {
	text := `Before tool call
<invoke name="read_file"><parameter name="path">README.md</parameter></invoke>
</tool_calls>
after`
	res := ParseToolCallsDetailed(text, []string{"read_file"})
	if len(res.Calls) != 1 {
		t.Fatalf("expected repaired wrapper to parse exactly one call, got %#v", res)
	}
	if res.Calls[0].Name != "read_file" {
		t.Fatalf("expected repaired wrapper to preserve tool name, got %#v", res.Calls[0])
	}
	if got, _ := res.Calls[0].Input["path"].(string); got != "README.md" {
		t.Fatalf("expected repaired wrapper to preserve args, got %#v", res.Calls[0].Input)
	}
	if !res.SawToolCallSyntax {
		t.Fatalf("expected repaired wrapper to mark tool syntax seen, got %#v", res)
	}
}

func TestParseToolCallsRejectsLegacyCanonicalBody(t *testing.T) {
	text := `<tool_calls><invoke name="read_file"><tool_name>read_file</tool_name><param>{"path":"README.md"}</param></invoke></tool_calls>`
	calls := ParseToolCalls(text, []string{"read_file"})
	if len(calls) != 0 {
		t.Fatalf("expected legacy canonical body to be rejected, got %#v", calls)
	}
}

func TestRepairInvalidJSONBackslashes(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{`{"path": "C:\Users\name"}`, `{"path": "C:\\Users\name"}`},
		{`{"cmd": "cd D:\git_codes"}`, `{"cmd": "cd D:\\git_codes"}`},
		{`{"text": "line1\nline2"}`, `{"text": "line1\nline2"}`},
		{`{"path": "D:\\back\\slash"}`, `{"path": "D:\\back\\slash"}`},
		{`{"unicode": "\u2705"}`, `{"unicode": "\u2705"}`},
		{`{"invalid_u": "\u123"}`, `{"invalid_u": "\\u123"}`},
	}

	for _, tt := range tests {
		got := repairInvalidJSONBackslashes(tt.input)
		if got != tt.expected {
			t.Errorf("repairInvalidJSONBackslashes(%s) = %s; want %s", tt.input, got, tt.expected)
		}
	}
}

func TestRepairLooseJSON(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{`{tool_calls: [{"name": "search", "input": {"q": "go"}}]}`, `{"tool_calls": [{"name": "search", "input": {"q": "go"}}]}`},
		{`{name: "search", input: {q: "go"}}`, `{"name": "search", "input": {"q": "go"}}`},
	}

	for _, tt := range tests {
		got := RepairLooseJSON(tt.input)
		if got != tt.expected {
			t.Errorf("RepairLooseJSON(%s) = %s; want %s", tt.input, got, tt.expected)
		}
	}
}

func TestParseToolCallInputRepairsControlCharsInPath(t *testing.T) {
	in := `{"path":"D:\tmp\new\readme.txt","content":"line1\nline2"}`
	parsed := parseToolCallInput(in)

	path, ok := parsed["path"].(string)
	if !ok {
		t.Fatalf("expected path string in parsed input, got %#v", parsed["path"])
	}
	if path != `D:\tmp\new\readme.txt` {
		t.Fatalf("expected repaired windows path, got %q", path)
	}

	content, ok := parsed["content"].(string)
	if !ok {
		t.Fatalf("expected content string in parsed input, got %#v", parsed["content"])
	}
	if content != "line1\nline2" {
		t.Fatalf("expected non-path field to keep decoded escapes, got %q", content)
	}
}

func TestRepairLooseJSONWithNestedObjects(t *testing.T) {
	// 测试嵌套对象的修复：DeepSeek 幻觉输出，每个元素内部包含嵌套 {}
	// 注意：正则只支持单层嵌套，不支持更深层次的嵌套
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		// 1. 单层嵌套对象（核心修复目标）
		{
			name:     "单层嵌套 - 2个元素",
			input:    `"todos": {"content": "研究算法", "input": {"q": "8 queens"}}, {"content": "实现", "input": {"path": "queens.py"}}`,
			expected: `"todos": [{"content": "研究算法", "input": {"q": "8 queens"}}, {"content": "实现", "input": {"path": "queens.py"}}]`,
		},
		// 2. 3个单层嵌套对象
		{
			name:     "3个单层嵌套对象",
			input:    `"items": {"a": {"x":1}}, {"b": {"y":2}}, {"c": {"z":3}}`,
			expected: `"items": [{"a": {"x":1}}, {"b": {"y":2}}, {"c": {"z":3}}]`,
		},
		// 3. 混合嵌套：有些字段是对象，有些是原始值
		{
			name:     "混合嵌套 - 对象和原始值混合",
			input:    `"items": {"name": "test", "config": {"timeout": 30}}, {"name": "test2", "config": {"timeout": 60}}`,
			expected: `"items": [{"name": "test", "config": {"timeout": 30}}, {"name": "test2", "config": {"timeout": 60}}]`,
		},
		// 4. 4个嵌套对象（边界测试）
		{
			name:     "4个嵌套对象",
			input:    `"todos": {"id": 1}, {"id": 2}, {"id": 3}, {"id": 4}`,
			expected: `"todos": [{"id": 1}, {"id": 2}, {"id": 3}, {"id": 4}]`,
		},
		// 5. DeepSeek 典型幻觉：无空格逗号分隔
		{
			name:     "无空格逗号分隔",
			input:    `"results": {"name": "a"}, {"name": "b"}, {"name": "c"}`,
			expected: `"results": [{"name": "a"}, {"name": "b"}, {"name": "c"}]`,
		},
		// 6. 嵌套数组（数组在对象内，不是深层嵌套）
		{
			name:     "对象内包含数组",
			input:    `"data": {"items": [1,2,3]}, {"items": [4,5,6]}`,
			expected: `"data": [{"items": [1,2,3]}, {"items": [4,5,6]}]`,
		},
		// 7. 真实的 DeepSeek 8皇后问题输出
		{
			name:     "DeepSeek 8皇后真实输出",
			input:    `"todos": {"content": "研究8皇后算法", "status": "pending"}, {"content": "实现Python脚本", "status": "pending"}, {"content": "验证结果", "status": "pending"}`,
			expected: `"todos": [{"content": "研究8皇后算法", "status": "pending"}, {"content": "实现Python脚本", "status": "pending"}, {"content": "验证结果", "status": "pending"}]`,
		},
		// 8. 简单无嵌套对象（回归测试）
		{
			name:     "简单无嵌套对象",
			input:    `"items": {"a": 1}, {"b": 2}`,
			expected: `"items": [{"a": 1}, {"b": 2}]`,
		},
		// 9. 更复杂的单层嵌套
		{
			name:     "复杂单层嵌套",
			input:    `"functions": {"name": "execute", "input": {"command": "ls"}}, {"name": "read", "input": {"file": "a.txt"}}`,
			expected: `"functions": [{"name": "execute", "input": {"command": "ls"}}, {"name": "read", "input": {"file": "a.txt"}}]`,
		},
		// 10. 5个嵌套对象
		{
			name:     "5个嵌套对象",
			input:    `"tasks": {"id":1}, {"id":2}, {"id":3}, {"id":4}, {"id":5}`,
			expected: `"tasks": [{"id":1}, {"id":2}, {"id":3}, {"id":4}, {"id":5}]`,
		},
	}

	for _, tt := range tests {
		got := RepairLooseJSON(tt.input)
		if got != tt.expected {
			t.Errorf("[%s] RepairLooseJSON with nested objects:\n  input:    %s\n  got:      %s\n  expected: %s", tt.name, tt.input, got, tt.expected)
		}
	}
}

func TestParseToolCallsUnescapesHTMLEntityArguments(t *testing.T) {
	text := `<tool_calls><invoke name="Bash"><parameter name="command">echo a &gt; out.txt</parameter></invoke></tool_calls>`
	calls := ParseToolCalls(text, []string{"bash"})
	if len(calls) != 1 {
		t.Fatalf("expected one call, got %#v", calls)
	}
	cmd, _ := calls[0].Input["command"].(string)
	if cmd != "echo a > out.txt" {
		t.Fatalf("expected html entities to be unescaped in command, got %q", cmd)
	}
}

func TestParseToolCallsIgnoresXMLInsideFencedCodeBlock(t *testing.T) {
	text := "Here is an example:\n```xml\n<tool_calls><invoke name=\"read_file\"><parameter name=\"path\">README.md</parameter></invoke></tool_calls>\n```\nDo not execute it."
	res := ParseToolCallsDetailed(text, []string{"read_file"})
	if len(res.Calls) != 0 {
		t.Fatalf("expected no parsed calls for fenced example, got %#v", res.Calls)
	}
}

func TestParseToolCallsParsesOnlyNonFencedXMLToolCall(t *testing.T) {
	text := "```xml\n<tool_calls><invoke name=\"read_file\"><parameter name=\"path\">README.md</parameter></invoke></tool_calls>\n```\n<tool_calls><invoke name=\"search\"><parameter name=\"q\">golang</parameter></invoke></tool_calls>"
	res := ParseToolCallsDetailed(text, []string{"read_file", "search"})
	if len(res.Calls) != 1 {
		t.Fatalf("expected exactly one parsed call outside fence, got %#v", res.Calls)
	}
	if res.Calls[0].Name != "search" {
		t.Fatalf("expected non-fenced tool call to be parsed, got %#v", res.Calls[0])
	}
}

func TestParseToolCallsParsesAfterFourBacktickFence(t *testing.T) {
	text := "````markdown\n```xml\n<tool_calls><invoke name=\"read_file\"><parameter name=\"path\">README.md</parameter></invoke></tool_calls>\n```\n````\n<tool_calls><invoke name=\"search\"><parameter name=\"q\">outside</parameter></invoke></tool_calls>"
	res := ParseToolCallsDetailed(text, []string{"read_file", "search"})
	if len(res.Calls) != 1 {
		t.Fatalf("expected exactly one parsed call outside four-backtick fence, got %#v", res.Calls)
	}
	if res.Calls[0].Name != "search" {
		t.Fatalf("expected non-fenced tool call to be parsed, got %#v", res.Calls[0])
	}
}

func TestParseToolCallsToleratesDSMLSpaceSeparatorTypo(t *testing.T) {
	text := strings.Join([]string{
		"<|DSML tool_calls>",
		"<|DSML invoke name=\"Read\">",
		"<|DSML parameter name=\"file_path\"><![CDATA[/tmp/input.txt]]></|DSML parameter>",
		"</|DSML invoke>",
		"</|DSML tool_calls>",
	}, "\n")
	calls := ParseToolCalls(text, []string{"Read"})
	if len(calls) != 1 {
		t.Fatalf("expected one call from DSML space-separator typo, got %#v", calls)
	}
	if calls[0].Name != "Read" {
		t.Fatalf("expected Read call, got %#v", calls[0])
	}
	if got, _ := calls[0].Input["file_path"].(string); got != "/tmp/input.txt" {
		t.Fatalf("expected file_path to parse, got %q", got)
	}
}

func TestParseToolCallsToleratesDSMLZeroWidthLowLineSeparators(t *testing.T) {
	sep := "\u200d\u2581"
	text := strings.Join([]string{
		"<\uff5cDSML" + sep + "tool_calls>",
		"<\uff5cDSML" + sep + "invoke name=\"web_search\">",
		"<\uff5cDSML" + sep + "parameter name=\"count\"><![CDATA[8]]></\uff5cDSML" + sep + "parameter>",
		"<\uff5cDSML" + sep + "parameter name=\"query\"><![CDATA[Chiang Mai cheapest international schools under 5000 USD 2025 2026 Panyaden Lanna ABS Ambassadorial bilingual quality]]></\uff5c",
		"DSML" + sep + "parameter>",
		"</\uff5cDSML" + sep + "invoke>",
		"<\uff5cDSML" + sep + "invoke name=\"web_search\">",
		"<\uff5cDSML" + sep + "parameter name=\"count\"><![CDATA[8]]></\uff5cDSML" + sep + "parameter>",
		"<\uff5cDSML" + sep + "parameter name=\"query\"><![CDATA[Spain public school quality ranking PISA 2025 foreign students integration English support]]></\uff5cDSML" + sep + "parameter>",
		"</\uff5cDSML" + sep + "invoke>",
		"<\uff5cDSML" + sep + "invoke name=\"web_search\">",
		"<\uff5cDSML" + sep + "parameter name=\"count\"><![CDATA[8]]></\uff5cDSML" + sep + "parameter>",
		"<\uff5cDSML" + sep + "parameter name=\"query\"><![CDATA[Portugal public school digital nomad children enrollment quality free education expat 2025 2026]]></\uff5cDSML" + sep + "parameter>",
		"</\uff5cDSML" + sep + "invoke>",
		"</\uff5cDSML" + sep + "tool_calls>",
	}, "\n")

	calls := ParseToolCalls(text, []string{"web_search"})
	if len(calls) != 3 {
		t.Fatalf("expected three calls from zero-width low-line DSML tags, got %#v", calls)
	}
	if calls[0].Name != "web_search" {
		t.Fatalf("expected web_search call, got %#v", calls[0])
	}
	if got, _ := calls[0].Input["query"].(string); !strings.Contains(got, "Chiang Mai cheapest") {
		t.Fatalf("expected query to parse, got %q", got)
	}
	if got := calls[0].Input["count"]; got != float64(8) {
		t.Fatalf("expected count to parse as number, got %#v", got)
	}
}

func TestParseToolCallsDoesNotAcceptDSMLZeroWidthLowLineLookalikeTagName(t *testing.T) {
	sep := "\u200d\u2581"
	text := strings.Join([]string{
		"<\uff5cDSML" + sep + "tool_calls_extra>",
		"<\uff5cDSML" + sep + "invoke name=\"web_search\">",
		"<\uff5cDSML" + sep + "parameter name=\"query\">x</\uff5cDSML" + sep + "parameter>",
		"</\uff5cDSML" + sep + "invoke>",
		"</\uff5cDSML" + sep + "tool_calls_extra>",
	}, "\n")
	calls := ParseToolCalls(text, []string{"web_search"})
	if len(calls) != 0 {
		t.Fatalf("expected no calls from zero-width lookalike tag, got %#v", calls)
	}
}

func TestParseToolCallsDoesNotAcceptTokenArtifactWithoutDSMLPrefix(t *testing.T) {
	text := "<\u200d\u2581tool_calls><invoke name=\"web_search\"><parameter name=\"query\">x</parameter></invoke></\u200d\u2581tool_calls>"
	calls := ParseToolCalls(text, []string{"web_search"})
	if len(calls) != 0 {
		t.Fatalf("expected no calls from token artifact without DSML prefix, got %#v", calls)
	}
}

func TestParseToolCallsDoesNotAcceptDSMLSpaceLookalikeTagName(t *testing.T) {
	text := strings.Join([]string{
		"<|DSML tool_calls_extra>",
		"<|DSML invoke name=\"Read\">",
		"<|DSML parameter name=\"file_path\">/tmp/input.txt</|DSML parameter>",
		"</|DSML invoke>",
		"</|DSML tool_calls_extra>",
	}, "\n")
	calls := ParseToolCalls(text, []string{"Read"})
	if len(calls) != 0 {
		t.Fatalf("expected no calls from lookalike tag, got %#v", calls)
	}
}

func TestParseToolCallsToleratesDSMLCollapsedTagNames(t *testing.T) {
	todos := `[x] 检查 toolcalls_format.go 格式化逻辑
[x] 检查 toolcalls_parse.go 解析逻辑
[x] 检查 toolcalls_xml.go 和 toolcalls_dsml.go
[x] 检查 toolcalls_markup.go 和 toolcalls_json_repair.go
[x] 检查 prompt/tool_calls.go 注入逻辑
[x] 检查 toolstream 流式解析
[x] 查看测试文件确认预期行为
[x] 给出调查结论`
	text := strings.Join([]string{
		"[]",
		"<DSMLtool_calls>",
		"<DSMLinvoke name=\"update_todo_list\">",
		"<DSMLparameter name=\"todos\"><![CDATA[" + todos + "]]></DSMLparameter>",
		"</DSMLinvoke>",
		"</DSMLtool_calls>",
	}, "\n")
	calls := ParseToolCalls(text, []string{"update_todo_list"})
	if len(calls) != 1 {
		t.Fatalf("expected one call from collapsed DSML tags, got %#v", calls)
	}
	if calls[0].Name != "update_todo_list" {
		t.Fatalf("expected update_todo_list call, got %#v", calls[0])
	}
	if got, _ := calls[0].Input["todos"].(string); got != todos {
		t.Fatalf("expected todos to round-trip, got %q", got)
	}
}

func TestParseToolCallsDoesNotAcceptDSMLCollapsedLookalikeTagName(t *testing.T) {
	text := strings.Join([]string{
		"<DSMLtool_calls_extra>",
		"<DSMLinvoke name=\"update_todo_list\">",
		"<DSMLparameter name=\"todos\">x</DSMLparameter>",
		"</DSMLinvoke>",
		"</DSMLtool_calls_extra>",
	}, "\n")
	calls := ParseToolCalls(text, []string{"update_todo_list"})
	if len(calls) != 0 {
		t.Fatalf("expected no calls from collapsed lookalike tag, got %#v", calls)
	}
}

func TestParseToolCallsSkipsProseMentionOfSameWrapperVariant(t *testing.T) {
	text := strings.Join([]string{
		"Summary: support canonical <tool_calls> and DSML <|DSML|tool_calls> wrappers.",
		"",
		"<|DSML|tool_calls>",
		"<|DSML|invoke name=\"Bash\">",
		"<|DSML|parameter name=\"command\"><![CDATA[git status]]></|DSML|parameter>",
		"</|DSML|invoke>",
		"</|DSML|tool_calls>",
	}, "\n")
	res := ParseToolCallsDetailed(text, []string{"Bash"})
	if len(res.Calls) != 1 {
		t.Fatalf("expected one parsed call after prose mention, got %#v", res.Calls)
	}
	if res.Calls[0].Name != "Bash" {
		t.Fatalf("expected Bash call, got %#v", res.Calls[0])
	}
	if got, _ := res.Calls[0].Input["command"].(string); got != "git status" {
		t.Fatalf("expected command to parse, got %q", got)
	}
}
