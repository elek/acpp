package net.anzix.acpp.data.remote

import net.anzix.acpp.data.remote.dto.LogEntry
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.channelFlow
import kotlinx.coroutines.isActive
import kotlinx.serialization.json.Json
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import javax.inject.Inject
import javax.inject.Named

/** A single update emitted by [EventSocket]. */
sealed interface SocketEvent {
    /** The socket (re)connected — consumers should resync from `/events`. */
    data object Connected : SocketEvent
    data class Entry(val entry: LogEntry) : SocketEvent
    data class Failure(val error: Throwable) : SocketEvent
}

/**
 * Streams a session's live events over the server WebSocket, reconnecting with
 * exponential backoff. Emits [SocketEvent.Connected] on each (re)connection so
 * the ViewModel can re-replay `/events` to resync.
 */
class EventSocket @Inject constructor(
    @Named("api") private val client: OkHttpClient,
    private val json: Json,
) {
    fun connect(sessionId: String): Flow<SocketEvent> = channelFlow {
        // Host/port are rewritten by HostSelectionInterceptor; path is what matters.
        val request = Request.Builder()
            .url("http://localhost/session/$sessionId/ws")
            .build()

        var backoffMs = INITIAL_BACKOFF_MS
        while (isActive) {
            val closed = CompletableDeferred<Unit>()
            var connectedOnce = false

            val listener = object : WebSocketListener() {
                override fun onOpen(webSocket: WebSocket, response: Response) {
                    connectedOnce = true
                    trySend(SocketEvent.Connected)
                }

                override fun onMessage(webSocket: WebSocket, text: String) {
                    val entry = runCatching { json.decodeFromString<LogEntry>(text) }.getOrNull()
                    if (entry != null) trySend(SocketEvent.Entry(entry))
                }

                override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
                    webSocket.close(NORMAL_CLOSURE, null)
                }

                override fun onClosed(webSocket: WebSocket, code: Int, reason: String) {
                    if (!closed.isCompleted) closed.complete(Unit)
                }

                override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
                    trySend(SocketEvent.Failure(t))
                    if (!closed.isCompleted) closed.complete(Unit)
                }
            }

            val webSocket = client.newWebSocket(request, listener)
            try {
                closed.await()
            } finally {
                webSocket.cancel()
            }

            if (!isActive) break
            backoffMs = if (connectedOnce) INITIAL_BACKOFF_MS else backoffMs
            delay(backoffMs)
            backoffMs = (backoffMs * 2).coerceAtMost(MAX_BACKOFF_MS)
        }
    }

    private companion object {
        const val NORMAL_CLOSURE = 1000
        const val INITIAL_BACKOFF_MS = 1_000L
        const val MAX_BACKOFF_MS = 15_000L
    }
}
