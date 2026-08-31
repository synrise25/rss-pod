package jobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	edgetts "github.com/foresturquhart/edge-tts"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/synrise25/rss-pod/internal/config"
	"github.com/synrise25/rss-pod/internal/storage"
	ttspkg "github.com/synrise25/rss-pod/internal/tts"
)

type GenerateTTSWorker struct {
	river.WorkerDefaults[GenerateTTSArgs]
	Pool    *pgxpool.Pool
	Config  *config.Config
	Storage *storage.Client
	River   *river.Client[pgx.Tx]
}

type scriptTurn struct {
	Position   int
	SpeakerID  string
	TTSService string
	Voice      string
	Talker     string
	Text       string
}

type episodeDialogue struct {
	ProfileID string
	config.DialogueProfile
}

func (w *GenerateTTSWorker) Work(ctx context.Context, job *river.Job[GenerateTTSArgs]) error {
	if _, err := w.Pool.Exec(ctx, `
		UPDATE episodes SET status = 'generating_tts', error = '', updated_at = now() WHERE id = $1
	`, job.Args.EpisodeID); err != nil {
		return fmt.Errorf("mark episode generating TTS: %w", err)
	}
	if err := w.generate(ctx, job.Args.EpisodeID); err != nil {
		return finishEpisodeAttempt(ctx, w.Pool, job.Args.EpisodeID, job.Attempt, job.MaxAttempts, err)
	}
	return nil
}

func (w *GenerateTTSWorker) generate(ctx context.Context, episodeID string) error {
	dialogue, err := w.loadDialogue(ctx, episodeID)
	if err != nil {
		return err
	}
	turns, err := w.loadTurns(ctx, episodeID)
	if err != nil {
		return err
	}
	existing, err := w.loadExistingSegments(ctx, episodeID)
	if err != nil {
		return err
	}
	if turns[0].Talker != "" {
		if err := w.generateMultiTalker(ctx, episodeID, turns, existing); err != nil {
			return err
		}
	} else {
		if err := w.generateSingleTalker(ctx, episodeID, dialogue.DialogueProfile, turns, existing); err != nil {
			return err
		}
	}

	tx, err := w.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin TTS completion transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		UPDATE episodes SET status = 'composing', error = '', updated_at = now() WHERE id = $1
	`, episodeID); err != nil {
		return fmt.Errorf("mark episode composing: %w", err)
	}
	if _, err := w.River.InsertTx(ctx, tx, ComposeEpisodeArgs{EpisodeID: episodeID}, nil); err != nil {
		return fmt.Errorf("enqueue episode composition: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit TTS completion: %w", err)
	}
	return nil
}

func (w *GenerateTTSWorker) generateSingleTalker(ctx context.Context, episodeID string, dialogue config.DialogueProfile, turns []scriptTurn, existing map[int]string) error {
	for _, turn := range turns {
		if turn.Talker != "" {
			return permanent("episode mixes MultiTalker and single-talker voices")
		}
		if _, ok := existing[turn.Position]; ok {
			continue
		}
		service, ok := w.Config.Services.TTS[turn.TTSService]
		if !ok {
			return permanent("episode speaker %s references unknown TTS service %q", turn.SpeakerID, turn.TTSService)
		}
		audio, err := synthesizeTurn(ctx, turn.TTSService, service, dialogue, turn.Voice, turn.Text)
		if err != nil {
			return err
		}
		key := fmt.Sprintf("episodes/%s/segments/%04d.mp3", episodeID, turn.Position)
		if err := w.Storage.PutPrivate(ctx, key, "audio/mpeg", audio); err != nil {
			return err
		}
		if _, err := w.Pool.Exec(ctx, `
			INSERT INTO audio_segments (episode_id, position, tts_service, object_key, byte_size)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (episode_id, position) DO UPDATE SET
			    tts_service = EXCLUDED.tts_service,
			    object_key = EXCLUDED.object_key,
			    byte_size = EXCLUDED.byte_size,
			    created_at = now()
		`, episodeID, turn.Position, turn.TTSService, key, len(audio)); err != nil {
			return fmt.Errorf("store audio segment: %w", err)
		}
	}
	return nil
}

func (w *GenerateTTSWorker) generateMultiTalker(ctx context.Context, episodeID string, turns []scriptTurn, existing map[int]string) error {
	const position = 0
	serviceName := turns[0].TTSService
	voiceName := turns[0].Voice
	azureTurns := make([]ttspkg.MultiTalkerTurn, len(turns))
	for i, turn := range turns {
		if turn.Talker == "" {
			return permanent("episode mixes MultiTalker and single-talker voices")
		}
		if turn.TTSService != serviceName || !strings.EqualFold(turn.Voice, voiceName) {
			return permanent("episode MultiTalker turns must use the same TTS service and voice model")
		}
		azureTurns[i] = ttspkg.MultiTalkerTurn{Speaker: turn.Talker, Text: turn.Text}
	}
	if serviceName != config.AzureTTSServiceName {
		return permanent("MultiTalker voice requires the Azure TTS service")
	}
	if _, ok := existing[position]; ok {
		return nil
	}
	service, ok := w.Config.Services.TTS[serviceName]
	if !ok {
		return permanent("episode references unknown TTS service %q", serviceName)
	}
	audio, err := ttspkg.SynthesizeAzureMultiTalker(ctx, service, voiceName, azureTurns)
	if err != nil {
		return classifyAzureError(err)
	}
	key := fmt.Sprintf("episodes/%s/segments/%04d.mp3", episodeID, position)
	if err := w.Storage.PutPrivate(ctx, key, "audio/mpeg", audio); err != nil {
		return err
	}
	if _, err := w.Pool.Exec(ctx, `
		INSERT INTO audio_segments (episode_id, position, tts_service, object_key, byte_size)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (episode_id, position) DO UPDATE SET
		    tts_service = EXCLUDED.tts_service,
		    object_key = EXCLUDED.object_key,
		    byte_size = EXCLUDED.byte_size,
		    created_at = now()
	`, episodeID, position, serviceName, key, len(audio)); err != nil {
		return fmt.Errorf("store MultiTalker audio segment: %w", err)
	}
	return nil
}

func (w *GenerateTTSWorker) loadDialogue(ctx context.Context, episodeID string) (episodeDialogue, error) {
	var dialogue episodeDialogue
	err := w.Pool.QueryRow(ctx, `
		SELECT profile_id, rate, volume, pitch
		FROM episode_dialogues WHERE episode_id = $1
	`, episodeID).Scan(&dialogue.ProfileID,
		&dialogue.Rate, &dialogue.Volume, &dialogue.Pitch)
	if errors.Is(err, pgx.ErrNoRows) {
		return episodeDialogue{}, permanent("episode has no dialogue snapshot")
	}
	if err != nil {
		return episodeDialogue{}, fmt.Errorf("load episode dialogue: %w", err)
	}
	return dialogue, nil
}

func (w *GenerateTTSWorker) loadTurns(ctx context.Context, episodeID string) ([]scriptTurn, error) {
	rows, err := w.Pool.Query(ctx, `
		SELECT turn.position, turn.speaker_id, speaker.tts_service, speaker.voice, turn.text
		FROM script_turns AS turn
		JOIN episode_speakers AS speaker
		  ON speaker.episode_id = turn.episode_id AND speaker.speaker_id = turn.speaker_id
		WHERE turn.episode_id = $1
		ORDER BY turn.position
	`, episodeID)
	if err != nil {
		return nil, fmt.Errorf("query script turns: %w", err)
	}
	defer rows.Close()
	var turns []scriptTurn
	for rows.Next() {
		var turn scriptTurn
		if err := rows.Scan(&turn.Position, &turn.SpeakerID, &turn.TTSService, &turn.Voice, &turn.Text); err != nil {
			return nil, err
		}
		voice, err := config.ParseSpeakerVoice(turn.TTSService + ":" + turn.Voice)
		if err != nil {
			return nil, permanent("episode speaker %s has invalid voice: %v", turn.SpeakerID, err)
		}
		turn.Voice = voice.Voice
		turn.Talker = voice.Talker
		turns = append(turns, turn)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(turns) == 0 {
		return nil, permanent("episode has no script turns")
	}
	return turns, nil
}

func (w *GenerateTTSWorker) loadExistingSegments(ctx context.Context, episodeID string) (map[int]string, error) {
	rows, err := w.Pool.Query(ctx, `SELECT position, tts_service FROM audio_segments WHERE episode_id = $1`, episodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[int]string)
	for rows.Next() {
		var position int
		var service string
		if err := rows.Scan(&position, &service); err != nil {
			return nil, err
		}
		result[position] = service
	}
	return result, rows.Err()
}

func synthesizeTurn(ctx context.Context, serviceName string, service config.TTSService, dialogue config.DialogueProfile, voice, text string) ([]byte, error) {
	switch serviceName {
	case config.EdgeTTSServiceName:
		return synthesizeEdge(ctx, service, dialogue, voice, text)
	case config.AzureTTSServiceName:
		audio, err := ttspkg.SynthesizeAzure(ctx, service, voice, text)
		if err == nil {
			return audio, nil
		}
		return nil, classifyAzureError(err)
	default:
		return nil, permanent("unsupported TTS service %q", serviceName)
	}
}

func classifyAzureError(err error) error {
	var responseErr *ttspkg.ResponseError
	if errors.As(err, &responseErr) && !responseErr.Retryable() {
		return permanent("Azure TTS request rejected: %v", err)
	}
	return fmt.Errorf("Azure TTS: %w", err)
}

func synthesizeEdge(ctx context.Context, service config.TTSService, dialogue config.DialogueProfile, voice, text string) ([]byte, error) {
	connectTimeout, err := time.ParseDuration(service.ConnectTimeout)
	if err != nil {
		return nil, permanent("invalid Edge TTS connect timeout: %v", err)
	}
	receiveTimeout, err := time.ParseDuration(service.ReceiveTimeout)
	if err != nil {
		return nil, permanent("invalid Edge TTS receive timeout: %v", err)
	}
	ttsConfig := edgetts.TTSConfig{
		Voice: voice, Rate: dialogue.Rate, Volume: dialogue.Volume, Pitch: dialogue.Pitch,
		Boundary: edgetts.SentenceBoundary,
	}
	options := []edgetts.CommunicateOption{
		edgetts.WithConnectTimeout(connectTimeout),
		edgetts.WithReceiveTimeout(receiveTimeout),
	}
	if service.Proxy != "" {
		options = append(options, edgetts.WithProxy(service.Proxy))
	}
	communicate, err := edgetts.NewCommunicate(text, ttsConfig, options...)
	if err != nil {
		return nil, permanent("invalid Edge TTS request: %v", err)
	}
	var audio bytes.Buffer
	if err := communicate.Stream(ctx, func(chunk edgetts.TTSChunk) error {
		if chunk.Type == edgetts.ChunkTypeAudio {
			_, err := audio.Write(chunk.Data)
			return err
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("Edge TTS stream: %w", err)
	}
	if audio.Len() == 0 {
		return nil, fmt.Errorf("Edge TTS returned no audio")
	}
	return audio.Bytes(), nil
}
