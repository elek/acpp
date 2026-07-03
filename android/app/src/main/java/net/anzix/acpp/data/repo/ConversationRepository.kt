package net.anzix.acpp.data.repo

import net.anzix.acpp.data.remote.EventSocket
import net.anzix.acpp.data.remote.AcppApi
import net.anzix.acpp.data.remote.SocketEvent
import net.anzix.acpp.data.remote.dto.LogEntry
import net.anzix.acpp.data.remote.dto.PromptRequest
import net.anzix.acpp.domain.model.Conversation
import kotlinx.coroutines.flow.Flow
import javax.inject.Inject
import javax.inject.Singleton

@Singleton
class ConversationRepository @Inject constructor(
    private val api: AcppApi,
    private val socket: EventSocket,
) {
    suspend fun snapshot(id: String): Conversation = api.session(id).toDomain()

    suspend fun history(id: String): List<LogEntry> = api.events(id)

    fun events(id: String): Flow<SocketEvent> = socket.connect(id)

    suspend fun sendPrompt(id: String, prompt: String) {
        api.sendPrompt(id, PromptRequest(prompt))
    }

    suspend fun stop(id: String) {
        api.stop(id)
    }
}
