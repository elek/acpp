package net.anzix.acpp.domain.model

// Domain models — UI-facing, decoupled from transport DTOs.

enum class ConversationStatus {
    RUNNING, DONE, ERROR, IDLE;

    companion object {
        fun fromApi(s: String): ConversationStatus = when (s) {
            "running" -> RUNNING
            "done" -> DONE
            "error" -> ERROR
            else -> IDLE
        }
    }
}

data class Project(
    val name: String,
    val dir: String,
    val agent: String,
    val branch: String,
    val dirty: Boolean,
    val chatCount: Int,
    val runningCount: Int,
)

data class Conversation(
    val id: String,
    val title: String,
    val status: ConversationStatus,
    val stopReason: String,
    val preview: String,
    val model: String,
    val contextUsed: Long,
    val contextWindow: Long,
    val costUsd: Double?,
    val createdAt: String,
    val updatedAt: String,
)

enum class Role { USER, ASSISTANT, SYSTEM, ERROR }

data class ToolCall(
    val id: String,
    val title: String = "",
    val kind: String = "",
    val status: String = "",
    val content: String = "",
)

/**
 * One ordered piece of an assistant turn. Keeping text, thoughts and tool calls
 * in a single ordered list (rather than separate fields) preserves the order in
 * which they streamed, so the UI can render `text → tool → text → tool …` as it
 * actually happened instead of bunching all tool calls at the end.
 */
sealed interface Block {
    data class Text(val text: String) : Block
    data class Thought(val text: String) : Block
    data class Tool(val call: ToolCall) : Block
}

data class Message(
    val role: Role,
    val text: String = "",
    val blocks: List<Block> = emptyList(),
)

data class PlanEntry(
    val content: String,
    val priority: String,
    val status: String,
)

data class Usage(
    val contextUsed: Long = 0,
    val contextWindow: Long = 0,
    val costUsd: Double? = null,
)

/**
 * Immutable conversation state produced by the EventReducer. [navigateTo] is a
 * one-shot effect (a new session id from `session_replaced`) the ViewModel
 * consumes and clears.
 */
data class ConversationState(
    val messages: List<Message> = emptyList(),
    val plan: List<PlanEntry> = emptyList(),
    val usage: Usage = Usage(),
    val running: Boolean = false,
    val navigateTo: String? = null,
)
