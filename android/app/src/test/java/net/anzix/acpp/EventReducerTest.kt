package net.anzix.acpp

import net.anzix.acpp.data.remote.EventReducer
import net.anzix.acpp.data.remote.dto.LogEntry
import net.anzix.acpp.domain.model.Block
import net.anzix.acpp.domain.model.Role
import kotlinx.serialization.json.Json
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class EventReducerTest {
    private val json = Json { ignoreUnknownKeys = true }
    private val reducer = EventReducer(json)

    private fun entry(type: String, payload: String): LogEntry =
        LogEntry(eventType = type, payload = json.parseToJsonElement(payload))

    private val turn = listOf(
        entry("prompt", """{"prompt":"Hello"}"""),
        entry(
            "tool_call",
            """{"sessionUpdate":"tool_call","toolCallId":"t1","title":"Read file","kind":"read","status":"pending","content":[{"type":"content","content":{"type":"text","text":"reading"}}]}""",
        ),
        entry("agent_message_chunk", """{"content":{"type":"text","text":"Hi "}}"""),
        entry("agent_message_chunk", """{"content":{"type":"text","text":"there"}}"""),
        entry("agent_thought_chunk", """{"content":{"type":"text","text":"thinking"}}"""),
        entry("tool_call_update", """{"toolCallId":"t1","status":"completed"}"""),
        entry("plan", """{"entries":[{"content":"do x","priority":"high","status":"pending"}]}"""),
        entry("usage_update", """{"used":1000,"size":200000,"cost":{"amount":0.5,"currency":"USD"}}"""),
        entry("prompt_finished", """{}"""),
        entry("text_message", """{"text":"⚠️ something failed"}"""),
    )

    @Test
    fun `folds a full turn into transcript state`() {
        val state = reducer.replay(turn)

        assertEquals(3, state.messages.size)

        val user = state.messages[0]
        assertEquals(Role.USER, user.role)
        assertEquals("Hello", user.text)

        val assistant = state.messages[1]
        assertEquals(Role.ASSISTANT, assistant.role)
        // Blocks preserve stream order: tool call arrived first, then text, then thought.
        assertEquals(3, assistant.blocks.size)
        val tool = assistant.blocks[0] as Block.Tool
        assertEquals("t1", tool.call.id)
        assertEquals("Read file", tool.call.title)
        assertEquals("completed", tool.call.status)
        assertEquals("Hi there", (assistant.blocks[1] as Block.Text).text)
        assertEquals("thinking", (assistant.blocks[2] as Block.Thought).text)

        val err = state.messages[2]
        assertEquals(Role.ERROR, err.role)

        assertEquals(1, state.plan.size)
        assertEquals("do x", state.plan[0].content)

        assertEquals(1000L, state.usage.contextUsed)
        assertEquals(200000L, state.usage.contextWindow)
        assertEquals(0.5, state.usage.costUsd!!, 0.0001)

        assertFalse(state.running)
    }

    @Test
    fun `prompt sets running and prompt_finished clears it`() {
        val afterPrompt = reducer.reduce(net.anzix.acpp.domain.model.ConversationState(), turn[0])
        assertTrue(afterPrompt.running)
        val finished = reducer.reduce(afterPrompt, entry("prompt_finished", "{}"))
        assertFalse(finished.running)
    }

    @Test
    fun `agent chunk with no open message creates an assistant message`() {
        val state = reducer.reduce(
            net.anzix.acpp.domain.model.ConversationState(),
            entry("agent_message_chunk", """{"content":{"type":"text","text":"solo"}}"""),
        )
        assertEquals(1, state.messages.size)
        assertEquals(Role.ASSISTANT, state.messages[0].role)
        assertEquals("solo", (state.messages[0].blocks.single() as Block.Text).text)
    }

    @Test
    fun `replay is deterministic`() {
        val a = reducer.replay(turn)
        val b = reducer.replay(turn)
        assertEquals(a, b)
    }

    @Test
    fun `session_replaced emits navigate effect`() {
        val state = reducer.reduce(
            net.anzix.acpp.domain.model.ConversationState(),
            entry("session_replaced", """{"new_session_id":"sess-2"}"""),
        )
        assertEquals("sess-2", state.navigateTo)
    }

    @Test
    fun `unknown event type is ignored`() {
        val before = net.anzix.acpp.domain.model.ConversationState()
        val after = reducer.reduce(before, entry("mystery", """{"x":1}"""))
        assertEquals(before, after)
        assertNull(after.navigateTo)
    }
}
