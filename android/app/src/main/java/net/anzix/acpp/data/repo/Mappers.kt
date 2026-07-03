package net.anzix.acpp.data.repo

import net.anzix.acpp.data.remote.dto.ProjectDto
import net.anzix.acpp.data.remote.dto.SessionDto
import net.anzix.acpp.domain.model.Conversation
import net.anzix.acpp.domain.model.ConversationStatus
import net.anzix.acpp.domain.model.Project

fun ProjectDto.toDomain(): Project = Project(
    name = name,
    dir = dir,
    agent = agent,
    branch = branch,
    dirty = dirty,
    chatCount = chatCount,
    runningCount = runningCount,
)

fun SessionDto.toDomain(): Conversation = Conversation(
    id = id,
    title = title,
    status = ConversationStatus.fromApi(status),
    stopReason = stopReason,
    preview = preview,
    model = model,
    contextUsed = contextUsed,
    contextWindow = contextWindow,
    costUsd = costUsd,
    createdAt = createdAt,
    updatedAt = updatedAt,
)
