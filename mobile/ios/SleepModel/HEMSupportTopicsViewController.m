//
//  HEMSupportTopicsViewController.m
//  Sense
//
//  Created by Jimmy Lu on 6/15/15.
//  Copyright (c) 2015 Hello. All rights reserved.
//

#import "Sense-Swift.h"

#import "HEMSupportTopicsViewController.h"
#import "HEMSupportUtil.h"
#import "HEMMainStoryboard.h"
#import "HEMAlertViewController.h"
#import "HEMSupportTopicDataSource.h"
#import "HEMActivityCoverView.h"
#import "HEMScreenUtils.h"
#import "HEMSettingsHeaderFooterView.h"

@interface HEMSupportTopicsViewController () <UITableViewDelegate, MFMailComposeViewControllerDelegate>

@property (weak, nonatomic) IBOutlet UITableView *tableView;
@property (strong, nonatomic) HEMSupportTopicDataSource* dataSource;
@property (weak, nonatomic) UIBarButtonItem* cancelItem;

@end

@implementation HEMSupportTopicsViewController

- (void)viewDidLoad {
    [super viewDidLoad];
    [self configureTableView];
}

- (void)configureTableView {
    [self setDataSource:[[HEMSupportTopicDataSource alloc] init]];
    [[self tableView] setDataSource:[self dataSource]];

    UIView* header = [[HEMSettingsHeaderFooterView alloc] initWithTopBorder:NO bottomBorder:NO];
    [header setHidden:YES];
    [[self tableView] setTableHeaderView:header];
    
    UIView* footer = [[HEMSettingsHeaderFooterView alloc] initWithTopBorder:NO bottomBorder:NO];
    [footer setHidden:YES];
    [[self tableView] setTableFooterView:footer];
    
    [[self tableView] applyStyle];
}

- (void)viewWillAppear:(BOOL)animated {
    [super viewWillAppear:animated];
    [self configureNavItem];
}

- (void)configureNavItem {
    if ([self isModal] && ![self cancelItem]) {
        NSString* cancelText = NSLocalizedString(@"actions.cancel", nil);
        UIBarButtonItem* cancelItem = [[UIBarButtonItem alloc] initWithTitle:cancelText
                                                                       style:UIBarButtonItemStylePlain
                                                                      target:self
                                                                      action:@selector(cancel)];
        [[self navigationItem] setLeftBarButtonItem:cancelItem];
        [self setCancelItem:cancelItem];
    }
}

- (void)cancel {
    [self dismissViewControllerAnimated:YES completion:nil];
}

- (void)viewDidAppear:(BOOL)animated {
    [super viewDidAppear:animated];
    if (![[self dataSource] isLoaded]) {
        [self loadData];
    }
}

- (void)loadData {
    HEMActivityCoverView* activityView = [[HEMActivityCoverView alloc] init];
    [activityView setBackgroundColor:[UIColor clearColor]];
    NSString* loadingText = NSLocalizedString(@"activity.loading", nil);
    [activityView showInView:[self tableView] withText:loadingText activity:YES completion:nil];
    
    __weak typeof(self) weakSelf = self;
    [[self dataSource] reloadData:^(NSError *error) {
        [[[weakSelf tableView] tableHeaderView] setHidden:NO];
        [[[weakSelf tableView] tableFooterView] setHidden:NO];
        [[weakSelf tableView] reloadData];
        
        [activityView dismissWithResultText:nil showSuccessMark:NO remove:YES completion:nil];
        if (error) {
            [SENAnalytics trackError:error];
        }
    }];
}

#pragma mark - MFMailComposeViewControllerDelegate

- (void)mailComposeController:(MFMailComposeViewController *)controller
          didFinishWithResult:(MFMailComposeResult)result
                        error:(NSError *)error {
    [controller dismissViewControllerAnimated:YES completion:^{
        if (result == MFMailComposeResultSent) {
            // Matches the old behaviour of leaving this screen once the
            // request had actually been submitted.
            [[self navigationController] popViewControllerAnimated:NO];
        }
    }];
}

#pragma mark - UITableViewDelegate

- (void)tableView:(UITableView *)tableView
  willDisplayCell:(UITableViewCell *)cell
forRowAtIndexPath:(NSIndexPath *)indexPath {
    [[cell textLabel] setText:[[self dataSource] displayNameForRowAtIndexPath:indexPath]];
    [cell showStyledAccessoryViewIfNone];
    [cell applyDetailAccessoryStyle];
    [cell applyStyle];
    [cell showStyledSelectionView];
}

- (void)tableView:(UITableView *)tableView didSelectRowAtIndexPath:(NSIndexPath *)indexPath {
    [tableView deselectRowAtIndexPath:indexPath animated:YES];
    
    // The topic list still comes from our own /v1/support endpoint; only the
    // destination changed from a Zendesk ticket to a support email carrying the
    // topic as its subject.
    NSString* value = [[self dataSource] topicForRowAtIndexPath:indexPath];
    [HEMSupportUtil sendEmailTo:NSLocalizedString(@"help.email.address", nil)
                    withSubject:value
                      attachLog:YES
                           from:self
                   mailDelegate:self];
}

- (void)scrollViewDidScroll:(UIScrollView *)scrollView {
    [[self shadowView] updateVisibilityWithContentOffset:[scrollView contentOffset].y];
}

#pragma mark - Clean up

- (void)dealloc {
    [[NSNotificationCenter defaultCenter] removeObserver:self];
    [_tableView setDelegate:nil];
    [_tableView setDataSource:nil];
}

@end
