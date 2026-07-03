package net.anzix.acpp.data.remote.dto

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject

// ---- /api/* response DTOs ----

@Serializable
data class HealthDto(
    val status: String = "",
    val version: String = "",
)

@Serializable
data class ProjectDto(
    val name: String = "",
    val dir: String = "",
    val agent: String = "",
    val branch: String = "",
    val dirty: Boolean = false,
    @SerialName("chat_count") val chatCount: Int = 0,
    @SerialName("running_count") val runningCount: Int = 0,
)

@Serializable
data class SessionDto(
    val id: String = "",
    val title: String = "",
    val status: String = "",
    @SerialName("stop_reason") val stopReason: String = "",
    val preview: String = "",
    val model: String = "",
    @SerialName("context_used") val contextUsed: Long = 0,
    @SerialName("context_window") val contextWindow: Long = 0,
    @SerialName("cost_usd") val costUsd: Double? = null,
    @SerialName("created_at") val createdAt: String = "",
    @SerialName("updated_at") val updatedAt: String = "",
)

// ---- request/response bodies ----

@Serializable
data class CreateSessionRequest(
    val dir: String,
    val prompt: String? = null,
)

@Serializable
data class CreateSessionResponse(
    val id: String = "",
    val dir: String = "",
)

@Serializable
data class PromptRequest(val prompt: String)

@Serializable
data class PromptResponse(val status: String = "")

// ---- event log entry (shared by /events history and the WebSocket) ----

@Serializable
data class LogEntry(
    val id: Long = 0,
    val time: String = "",
    @SerialName("event_type") val eventType: String = "",
    val payload: JsonElement = JsonObject(emptyMap()),
)

// ---- ACP payload subsets parsed by the EventReducer ----

@Serializable
data class PromptPayload(val prompt: String = "")

@Serializable
data class TextMessagePayload(val text: String = "")

@Serializable
data class SessionReplacedPayload(
    @SerialName("new_session_id") val newSessionId: String = "",
)

@Serializable
data class ContentBlockDto(
    val type: String = "",
    val text: String = "",
)

@Serializable
data class ChunkPayload(
    val content: ContentBlockDto = ContentBlockDto(),
)

@Serializable
data class ToolCallContentDto(
    val type: String = "",
    val content: ContentBlockDto? = null, // type == "content"
    val path: String? = null,             // type == "diff"
    val newText: String? = null,
    val terminalId: String? = null,       // type == "terminal"
)

@Serializable
data class ToolCallPayload(
    val toolCallId: String = "",
    val title: String = "",
    val kind: String = "",
    val status: String = "",
    val content: List<ToolCallContentDto> = emptyList(),
)

@Serializable
data class ToolCallUpdatePayload(
    val toolCallId: String = "",
    val title: String? = null,
    val kind: String? = null,
    val status: String? = null,
    val content: List<ToolCallContentDto>? = null,
)

@Serializable
data class PlanEntryDto(
    val content: String = "",
    val priority: String = "",
    val status: String = "",
)

@Serializable
data class PlanPayload(
    val entries: List<PlanEntryDto> = emptyList(),
)

@Serializable
data class CostDto(
    val amount: Double = 0.0,
    val currency: String = "USD",
)

@Serializable
data class UsagePayload(
    val used: Int = 0,
    val size: Int = 0,
    val cost: CostDto? = null,
)
