package database

import (
	"context"
	"database/sql"
	"fmt"
)

type defaultService struct {
	name         string
	slug         string
	description  string
	requirements []string
}

var defaultServices = []defaultService{
	{name: "Field Report", slug: "field-report", description: "Uandaaji na uhariri wa field report.", requirements: []string{"Topic au title ya field report", "Maelezo ya field work au data uliyokusanya", "Guidelines za chuo, kama zipo"}},
	{name: "Research Proposal", slug: "research-proposal", description: "Uandaaji wa research proposal, topic, objectives, na methodology.", requirements: []string{"Topic au eneo la utafiti", "Objectives au research questions", "Guidelines za chuo au department", "References au proposal ya awali, kama ipo"}},
	{name: "Full Research", slug: "full-research", description: "Uandaaji wa full research kuanzia proposal hadi final report.", requirements: []string{"Proposal iliyokubaliwa", "Research data au questionnaires", "Comments za supervisor, kama zipo", "Guidelines za formatting za chuo"}},
	{name: "Maombi ya HESLB", slug: "heslb-application", description: "Msaada wa maandalizi ya maombi ya HESLB.", requirements: []string{"NIDA au birth certificate ya mwombaji", "Admission letter", "Vyeti au matokeo ya masomo", "Taarifa za mzazi au mlezi"}},
	{name: "Maombi ya Vyuo", slug: "college-application", description: "Msaada wa maombi ya vyuo na nyaraka zake.", requirements: []string{"Vyeti na transcripts", "NIDA au birth certificate", "Passport-size photo", "Taarifa za kozi na chuo unacholenga"}},
	{name: "Cheti cha Kuzaliwa", slug: "birth-certificate", description: "Msaada wa maombi ya cheti cha kuzaliwa.", requirements: []string{"Birth notification au taarifa ya hospitali", "NIDA ya mwombaji au mzazi", "Taarifa kamili za kuzaliwa", "Nyaraka nyingine ulizopewa na mamlaka"}},
}

func SeedDefaultServices(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `
		UPDATE services SET is_active = 0 WHERE slug = 'research'`); err != nil {
		return fmt.Errorf("deactivate legacy research service: %w", err)
	}
	for _, service := range defaultServices {
		var serviceExists bool
		if err := db.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM services WHERE slug = ?)`, service.slug,
		).Scan(&serviceExists); err != nil {
			return fmt.Errorf("check service %q: %w", service.name, err)
		}
		if !serviceExists {
			if _, err := db.ExecContext(ctx, `
				INSERT INTO services (name, slug, description)
				VALUES (?, ?, ?)`, service.name, service.slug, service.description); err != nil {
				return fmt.Errorf("seed service %q: %w", service.name, err)
			}
		}

		var serviceID int64
		if err := db.QueryRowContext(ctx, `SELECT id FROM services WHERE slug = ?`, service.slug).Scan(&serviceID); err != nil {
			return fmt.Errorf("find seeded service %q: %w", service.name, err)
		}
		if _, err := db.ExecContext(ctx, `DELETE FROM service_requirements WHERE service_id = ?`, serviceID); err != nil {
			return fmt.Errorf("reset requirements for service %q: %w", service.name, err)
		}
		for index, requirement := range service.requirements {
			if _, err := db.ExecContext(ctx, `
				INSERT INTO service_requirements (service_id, label, sort_order)
				VALUES (?, ?, ?)`, serviceID, requirement, index); err != nil {
				return fmt.Errorf("seed requirement for service %q: %w", service.name, err)
			}
		}
	}
	return nil
}
