package net.anzix.acpp.data.remote

import net.anzix.acpp.data.remote.dto.ChunkPayload
import net.anzix.acpp.data.remote.dto.LogEntry
import net.anzix.acpp.data.remote.dto.PlanPayload
import net.anzix.acpp.data.remote.dto.PromptPayload
import net.anzix.acpp.data.remote.dto.SessionReplacedPayload
import net.anzix.acpp.data.remote.dto.TextMessagePayload
import net.anzix.acpp.data.remote.dto.ToolCallContentDto
import net.anzix.acpp.data.remote.dto.ToolCallPayload
import net.anzix.acpp.data.remote.dto.ToolCallUpdatePayload
import net.anzix.acpp.data.remote.dto.UsagePayload
import net.anzix.acpp.domain.model.Block
import net.anzix.acpp.domain.model.ConversationState
import net.anzix.acpp.domain.model.Message
import net.anzix.acpp.domain.model.PlanEntry
import net.anzix.acpp.domain.model.Role
import net.anzix.acpp.domain.model.ToolCall
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.decodeFromJsonElement

/**
 * Pure fold of the `{event_type, payload}` event stream into [ConversationState].
 *
 * The same reducer consumes the `/events` history and the live WebSocket, so
 * replaying history reconstructs the transcript identically. This is the primary
 * unit-test target — keep it free of Android dependencies.
 */
class EventReducer(
    private val json: Json = Json { ignoreUnknownKeys = true },
) {
    fun replay(
        entries: List<LogEntry>,
        initial: ConversationState = ConversationState(),
    ): ConversationState = entries.fold(initial, ::reduce)

    fun reduce(state: ConversationState, entry: LogEntry): ConversationState =
        when (entry.eventType) {
            "prompt" -> {
                val p = decode<PromptPayload>(entry)
                state.copy(
                    messages = state.messages + Message(Role.USER, text = p.prompt),
                    running = true,
                )
            }

            "agent_message_chunk" -> {
                val c = decode<ChunkPayload>(entry)
                appendToAssistant(state) { blocks ->
                    val last = blocks.lastOrNull()
                    if (last is Block.Text) {
                        blocks.dropLast(1) + Block.Text(last.text + c.content.text)
                    } else {
                        blocks + Block.Text(c.content.text)
                    }
                }
            }

            "agent_thought_chunk" -> {
                val c = decode<ChunkPayload>(entry)
                appendToAssistant(state) { blocks ->
                    val last = blocks.lastOrNull()
                    if (last is Block.Thought) {
                        blocks.dropLast(1) + Block.Thought(last.text + c.content.text)
                    } else {
                        blocks + Block.Thought(c.content.text)
                    }
                }
            }

            "tool_call" -> {
                val t = decode<ToolCallPayload>(entry)
                val tc = ToolCall(
                    id = t.toolCallId,
                    title = t.title,
                    kind = t.kind,
                    status = t.status,
                    content = renderContent(t.content),
                )
                appendToAssistant(state) { blocks -> blocks + Block.Tool(tc) }
            }

            "tool_call_update" -> {
                val u = decode<ToolCallUpdatePayload>(entry)
                mergeToolCall(state, u)
            }

            "plan" -> {
                val p = decode<PlanPayload>(entry)
                state.copy(plan = p.entries.map { PlanEntry(it.content, it.priority, it.status) })
            }

            "usage_update" -> {
                val u = decode<UsagePayload>(entry)
                state.copy(
                    usage = state.usage.copy(
                        contextUsed = u.used.toLong(),
                        contextWindow = u.size.toLong(),
                        costUsd = u.cost?.amount ?: state.usage.costUsd,
                    ),
                )
            }

            "prompt_finished" -> state.copy(running = false)

            "text_message" -> {
                val m = decode<TextMessagePayload>(entry)
                val role = if (m.text.contains("⚠")) Role.ERROR else Role.SYSTEM
                state.copy(messages = state.messages + Message(role, text = m.text))
            }

            "session_replaced" -> {
                val r = decode<SessionReplacedPayload>(entry)
                state.copy(navigateTo = r.newSessionId)
            }

            else -> state
        }

    private inline fun <reified T> decode(entry: LogEntry): T =
        json.decodeFromJsonElement(entry.payload)

    /**
     * Applies [transform] to the current open assistant message's ordered blocks,
     * creating a fresh trailing assistant message when the last message isn't one
     * (e.g. a tool call arrives before any text chunk, or right after a prompt).
     */
    private fun appendToAssistant(
        state: ConversationState,
        transform: (List<Block>) -> List<Block>,
    ): ConversationState {
        val msgs = state.messages.toMutableList()
        if (msgs.isEmpty() || msgs.last().role != Role.ASSISTANT) {
            msgs.add(Message(Role.ASSISTANT))
        }
        val last = msgs.last()
        msgs[msgs.lastIndex] = last.copy(blocks = transform(last.blocks))
        return state.copy(messages = msgs)
    }

    private fun mergeToolCall(
        state: ConversationState,
        u: ToolCallUpdatePayload,
    ): ConversationState {
        val msgs = state.messages.toMutableList()
        for (mi in msgs.indices) {
            val blocks = msgs[mi].blocks
            val bi = blocks.indexOfFirst { it is Block.Tool && it.call.id == u.toolCallId }
            if (bi < 0) continue
            val old = (blocks[bi] as Block.Tool).call
            val merged = old.copy(
                title = u.title ?: old.title,
                kind = u.kind ?: old.kind,
                status = u.status ?: old.status,
                content = if (u.content != null) renderContent(u.content) else old.content,
            )
            val newBlocks = blocks.toMutableList().also { it[bi] = Block.Tool(merged) }
            msgs[mi] = msgs[mi].copy(blocks = newBlocks)
            return state.copy(messages = msgs)
        }
        return state
    }

    private fun renderContent(items: List<ToolCallContentDto>): String =
        items.mapNotNull { item ->
            when {
                item.content != null -> item.content.text.ifBlank { null }
                item.path != null -> item.path
                item.terminalId != null -> "terminal:${item.terminalId}"
                else -> null
            }
        }.joinToString("\n").trim()
}
