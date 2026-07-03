package net.anzix.acpp.data.remote

import net.anzix.acpp.data.remote.dto.CreateSessionRequest
import net.anzix.acpp.data.remote.dto.CreateSessionResponse
import net.anzix.acpp.data.remote.dto.HealthDto
import net.anzix.acpp.data.remote.dto.LogEntry
import net.anzix.acpp.data.remote.dto.ProjectDto
import net.anzix.acpp.data.remote.dto.PromptRequest
import net.anzix.acpp.data.remote.dto.PromptResponse
import net.anzix.acpp.data.remote.dto.SessionDto
import okhttp3.ResponseBody
import retrofit2.Response
import retrofit2.http.Body
import retrofit2.http.GET
import retrofit2.http.POST
import retrofit2.http.Path
import retrofit2.http.Query

interface AcppApi {
    @GET("api/health")
    suspend fun health(): HealthDto

    @GET("api/projects")
    suspend fun projects(): List<ProjectDto>

    @GET("api/sessions")
    suspend fun sessions(@Query("project") project: String? = null): List<SessionDto>

    @GET("api/session/{id}")
    suspend fun session(@Path("id") id: String): SessionDto

    @GET("session/{id}/events")
    suspend fun events(@Path("id") id: String): List<LogEntry>

    @POST("projects/session")
    suspend fun createProjectSession(@Body body: CreateSessionRequest): CreateSessionResponse

    @POST("session/{id}/prompt")
    suspend fun sendPrompt(@Path("id") id: String, @Body body: PromptRequest): PromptResponse

    // Returns 303 for HTML clients; the app ignores the body. Redirects are
    // disabled on the API client so this resolves to the raw 303.
    @POST("session/{id}/stop")
    suspend fun stop(@Path("id") id: String): Response<ResponseBody>
}
