package net.anzix.acpp

import net.anzix.acpp.data.settings.UrlNormalizer
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class UrlNormalizerTest {
    @Test
    fun `prepends http scheme when missing`() {
        assertEquals("http://10.0.2.2:8080/", UrlNormalizer.normalize("10.0.2.2:8080"))
    }

    @Test
    fun `keeps https scheme`() {
        assertEquals("https://example.com/", UrlNormalizer.normalize("https://example.com"))
    }

    @Test
    fun `adds trailing slash`() {
        assertEquals("http://host:9000/", UrlNormalizer.normalize("http://host:9000"))
    }

    @Test
    fun `trims whitespace`() {
        assertEquals("http://host/", UrlNormalizer.normalize("  http://host  "))
    }

    @Test
    fun `preserves existing path with trailing slash`() {
        assertEquals("http://host/api/", UrlNormalizer.normalize("http://host/api/"))
    }

    @Test
    fun `rejects blank input`() {
        assertNull(UrlNormalizer.normalize(""))
        assertNull(UrlNormalizer.normalize("   "))
    }
}
