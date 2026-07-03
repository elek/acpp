package net.anzix.acpp

import net.anzix.acpp.data.remote.dto.LogEntry
import net.anzix.acpp.data.remote.dto.ProjectDto
import net.anzix.acpp.data.remote.dto.SessionDto
import kotlinx.serialization.json.Json
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class DtoParsingTest {
    private val json = Json { ignoreUnknownKeys = true; explicitNulls = false; coerceInputValues = true }

    @Test
    fun `parses project json`() {
        val p = json.decodeFromString<ProjectDto>(
            """{"name":"acpp","dir":"/home/elek/p/acpp","agent":"claude","branch":"main","dirty":true,"chat_count":3,"running_count":1}""",
        )
        assertEquals("acpp", p.name)
        assertEquals("main", p.branch)
        assertEquals(true, p.dirty)
        assertEquals(3, p.chatCount)
        assertEquals(1, p.runningCount)
    }

    @Test
    fun `parses session json with snake_case fields`() {
        val s = json.decodeFromString<SessionDto>(
            """{"id":"sess_1","title":"Add /help","status":"done","stop_reason":"end_turn","preview":"...","model":"claude-sonnet-4.6","context_used":103800,"context_window":200000,"cost_usd":1.27,"created_at":"2026-06-28T00:00:00Z","updated_at":"2026-06-28T01:00:00Z"}""",
        )
        assertEquals("sess_1", s.id)
        assertEquals("done", s.status)
        assertEquals("end_turn", s.stopReason)
        assertEquals(103800L, s.contextUsed)
        assertEquals(1.27, s.costUsd!!, 0.0001)
    }

    @Test
    fun `null cost_usd parses as null`() {
        val s = json.decodeFromString<SessionDto>(
            """{"id":"x","status":"idle","cost_usd":null}""",
        )
        assertNull(s.costUsd)
    }

    @Test
    fun `parses log entry keeping payload as element`() {
        val e = json.decodeFromString<LogEntry>(
            """{"id":5,"time":"15:04:05.000","event_type":"prompt","payload":{"prompt":"hi"}}""",
        )
        assertEquals("prompt", e.eventType)
        assertEquals(5L, e.id)
    }
}
