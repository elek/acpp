package net.anzix.acpp.data.repo

import net.anzix.acpp.data.remote.AcppApi
import net.anzix.acpp.data.remote.dto.CreateSessionRequest
import net.anzix.acpp.domain.model.Conversation
import javax.inject.Inject
import javax.inject.Singleton

@Singleton
class SessionsRepository @Inject constructor(
    private val api: AcppApi,
) {
    suspend fun sessions(project: String?): List<Conversation> =
        api.sessions(project).map { it.toDomain() }

    suspend fun session(id: String): Conversation = api.session(id).toDomain()

    /** Creates a session for [dir], optionally sending an initial prompt. */
    suspend fun createSession(dir: String, prompt: String? = null): String =
        api.createProjectSession(CreateSessionRequest(dir = dir, prompt = prompt)).id
}
