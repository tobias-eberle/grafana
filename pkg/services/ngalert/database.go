package ngalert

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/grafana/grafana/pkg/services/sqlstore"
)

func getAlertDefinitionByID(alertDefinitionID int64, sess *sqlstore.DBSession) (*AlertDefinition, error) {
	alertDefinition := AlertDefinition{}
	has, err := sess.ID(alertDefinitionID).Get(&alertDefinition)
	if !has {
		return nil, errAlertDefinitionNotFound
	}
	if err != nil {
		return nil, err
	}
	return &alertDefinition, nil
}

// deleteAlertDefinitionByID is a handler for deleting an alert definition.
// It returns models.ErrAlertDefinitionNotFound if no alert definition is found for the provided ID.
func (ng *AlertNG) deleteAlertDefinitionByID(query *deleteAlertDefinitionByIDQuery) error {
	return ng.SQLStore.WithTransactionalDbSession(context.Background(), func(sess *sqlstore.DBSession) error {
		res, err := sess.Exec("DELETE FROM alert_definition WHERE id = ?", query.ID)
		if err != nil {
			return err
		}

		rowsAffected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		query.RowsAffected = rowsAffected
		return nil
	})
}

// getAlertDefinitionByID is a handler for retrieving an alert definition from that database by its ID.
// It returns models.ErrAlertDefinitionNotFound if no alert definition is found for the provided ID.
func (ng *AlertNG) getAlertDefinitionByID(query *getAlertDefinitionByIDQuery) error {
	return ng.SQLStore.WithTransactionalDbSession(context.Background(), func(sess *sqlstore.DBSession) error {
		alertDefinition, err := getAlertDefinitionByID(query.ID, sess)
		if err != nil {
			return err
		}
		query.Result = alertDefinition
		return nil
	})
}

// saveAlertDefinition is a handler for saving a new alert definition.
func (ng *AlertNG) saveAlertDefinition(cmd *saveAlertDefinitionCommand) error {
	return ng.SQLStore.WithTransactionalDbSession(context.Background(), func(sess *sqlstore.DBSession) error {
		alertDefinition := &AlertDefinition{
			OrgId:     cmd.OrgID,
			Name:      cmd.Name,
			Condition: cmd.Condition.RefID,
			Data:      cmd.Condition.QueriesAndExpressions,
		}

		if err := ng.validateAlertDefinition(alertDefinition, cmd.SignedInUser, cmd.SkipCache); err != nil {
			return err
		}

		if err := alertDefinition.preSave(); err != nil {
			return err
		}

		if _, err := sess.Insert(alertDefinition); err != nil {
			return err
		}

		cmd.Result = alertDefinition
		return nil
	})
}

// updateAlertDefinition is a handler for updating an existing alert definition.
// It returns models.ErrAlertDefinitionNotFound if no alert definition is found for the provided ID.
func (ng *AlertNG) updateAlertDefinition(cmd *updateAlertDefinitionCommand) error {
	return ng.SQLStore.WithTransactionalDbSession(context.Background(), func(sess *sqlstore.DBSession) error {
		alertDefinition := &AlertDefinition{
			Id:        cmd.ID,
			Name:      cmd.Name,
			Condition: cmd.Condition.RefID,
			Data:      cmd.Condition.QueriesAndExpressions,
		}

		if err := ng.validateAlertDefinition(alertDefinition, cmd.SignedInUser, cmd.SkipCache); err != nil {
			return err
		}

		if err := alertDefinition.preSave(); err != nil {
			return err
		}

		affectedRows, err := sess.ID(cmd.ID).Update(alertDefinition)
		if err != nil {
			return err
		}

		cmd.Result = alertDefinition
		cmd.RowsAffected = affectedRows
		return nil
	})
}

// getAlertDefinitions is a handler for retrieving alert definitions of specific organisation.
func (ng *AlertNG) getAlertDefinitions(cmd *listAlertDefinitionsCommand) error {
	return ng.SQLStore.WithTransactionalDbSession(context.Background(), func(sess *sqlstore.DBSession) error {
		alertDefinitions := make([]*AlertDefinition, 0)
		q := "SELECT * FROM alert_definition WHERE org_id = ?"
		if err := sess.SQL(q, cmd.OrgID).Find(&alertDefinitions); err != nil {
			return err
		}

		cmd.Result = alertDefinitions
		return nil
	})
}

// saveAlertDefinition is a handler for saving a new alert definition.
func (ng *AlertNG) saveAlertInstance(cmd *saveAlertInstanceCommand) error {
	return ng.SQLStore.WithDbSession(context.Background(), func(sess *sqlstore.DBSession) error {

		labelTupleJSON, labelsHash, err := SetLabelsHash(cmd.Labels)
		if err != nil {
			return err
		}

		alertInstance := &AlertInstance{
			OrgID:             cmd.OrgID,
			Labels:            cmd.Labels,
			LabelsHash:        labelsHash,
			CurrentState:      cmd.State,
			AlertDefinitionID: cmd.AlertDefinitionID,
			CurrentStateSince: time.Now(),
			LastEvalTime:      time.Now(), // TODO: Probably better to pass in to the command for more accurate timestamp
		}

		if err := ng.validateAlertInstance(alertInstance); err != nil {
			return err
		}

		s := strings.Builder{}

		dName := ng.SQLStore.Dialect.DriverName()

		// This is the wrong way I imagine....
		switch dName {
		// sqlite3 on conflict syntax is relatively new (3.24.0 / 2018)
		case "sqlite3", "postgres":
			s.WriteString(`INSERT INTO alert_instance
			(org_id, alert_definition_id, labels, labels_hash, current_state, current_state_since, last_eval_time)
				VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(alert_definition_id, labels_hash) DO UPDATE SET
				org_id=excluded.org_id,
				alert_definition_id=excluded.alert_definition_id,
				labels=excluded.labels,
				labels_hash=excluded.labels_hash,
				current_state=excluded.current_state,
				current_state_since=excluded.current_state_since,
				last_eval_time=excluded.last_eval_time
				`)

			// if dName == "postgres" {
			// 	s.WriteString(` RETURNING *`)
			// }

		case "mysql":
			s.WriteString(`INSERT INTO alert_instance
			(org_id, alert_definition_id, labels, labels_hash, current_state, current_state_since, last_eval_time)
				VALUES (?, ?, ?, ?, ?, ?, ?)
			ON DUPLICATE KEY UPDATE
				org_id=VALUES(org_id),
				alert_definition_id=VALUES(alert_definition_id),
				labels=VALUES(labels),
				labels_hash=VALUES(labels_hash),
				current_state=VALUES(current_state),
				current_state_since=VALUES(current_state_since),
				last_eval_time=VALUES(last_eval_time);
			`)

		default:
			return fmt.Errorf("unsupported database type for alert instances: %v", ng.SQLStore.Dialect.DriverName())
		}

		params := append(make([]interface{}, 0), cmd.OrgID, cmd.AlertDefinitionID, labelTupleJSON, alertInstance.LabelsHash, cmd.State, time.Now(), time.Now())

		_, err = sess.SQL(s.String(), params...).Query()
		if err != nil {
			return err
		}

		return nil
	})
}

// getAlertDefinitions is a handler for retrieving alert definitions of specific organisation.
func (ng *AlertNG) getAlertInstance(cmd *getAlertInstanceCommand) error {
	return ng.SQLStore.WithDbSession(context.Background(), func(sess *sqlstore.DBSession) error {
		instance := AlertInstance{}
		s := strings.Builder{}
		s.WriteString(`SELECT * FROM alert_instance
			WHERE
				org_id=? AND
				alert_definition_id=? AND
				labels_hash=?
		`)

		_, hash, err := SetLabelsHash(cmd.Labels)
		if err != nil {
			return err
		}

		params := append(make([]interface{}, 0), cmd.OrgID, cmd.AlertDefinitionID, hash)

		has, err := sess.SQL(s.String(), params...).Get(&instance)
		if !has {
			return fmt.Errorf("instance not found for labels %v (hash: %v), alert defintion id %v", cmd.Labels, hash, cmd.AlertDefinitionID)
		}
		if err != nil {
			return err
		}

		cmd.Result = &instance
		return nil
	})
}
